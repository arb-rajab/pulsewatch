import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest';
import { connectLiveEvents } from './liveEvents';

// A minimal EventSource stub: real EventSource isn't available in the
// vitest/node environment, so this fake tracks every instance created and
// lets tests drive its onopen/onerror/message dispatch directly — enough
// to prove connectLiveEvents' own reconnect/backoff logic (the part this
// module actually owns) without needing a real network stack.
class FakeEventSource {
	static instances: FakeEventSource[] = [];
	url: string;
	onopen: (() => void) | null = null;
	onerror: (() => void) | null = null;
	closed = false;
	listeners = new Map<string, ((e: MessageEvent<string>) => void)[]>();

	constructor(url: string) {
		this.url = url;
		FakeEventSource.instances.push(this);
	}
	addEventListener(type: string, listener: (e: MessageEvent<string>) => void) {
		const list = this.listeners.get(type) ?? [];
		list.push(listener);
		this.listeners.set(type, list);
	}
	close() {
		this.closed = true;
	}
	emit(type: string, data: unknown) {
		for (const listener of this.listeners.get(type) ?? []) {
			listener({ data: JSON.stringify(data) } as MessageEvent<string>);
		}
	}
}

beforeEach(() => {
	vi.useFakeTimers();
	FakeEventSource.instances = [];
	vi.stubGlobal('EventSource', FakeEventSource);
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

describe('connectLiveEvents', () => {
	it('delivers a real target_status event to the caller', () => {
		const onEvent = vi.fn();
		const handle = connectLiveEvents(onEvent);

		const source = FakeEventSource.instances[0];
		source.emit('target_status', {
			type: 'target_status',
			target_id: 't-1',
			state: 'alerting',
			streak: 3
		});

		expect(onEvent).toHaveBeenCalledWith(
			expect.objectContaining({ type: 'target_status', target_id: 't-1', state: 'alerting' })
		);
		handle.close();
	});

	it('reconnects with exponential backoff after repeated errors, resetting on success', () => {
		const onStatusChange = vi.fn();
		const handle = connectLiveEvents(() => {}, onStatusChange);

		expect(FakeEventSource.instances).toHaveLength(1);

		// First failure: reconnect scheduled at the base delay (1s).
		FakeEventSource.instances[0].onerror?.();
		expect(onStatusChange).toHaveBeenLastCalledWith('reconnecting');
		vi.advanceTimersByTime(999);
		expect(FakeEventSource.instances).toHaveLength(1); // not yet
		vi.advanceTimersByTime(1);
		expect(FakeEventSource.instances).toHaveLength(2); // reconnected

		// Second failure without ever succeeding: backoff doubles to 2s.
		FakeEventSource.instances[1].onerror?.();
		vi.advanceTimersByTime(1999);
		expect(FakeEventSource.instances).toHaveLength(2);
		vi.advanceTimersByTime(1);
		expect(FakeEventSource.instances).toHaveLength(3);

		// A successful open resets the backoff back to the base delay.
		FakeEventSource.instances[2].onopen?.();
		expect(onStatusChange).toHaveBeenLastCalledWith('connected');
		FakeEventSource.instances[2].onerror?.();
		vi.advanceTimersByTime(1000);
		expect(FakeEventSource.instances).toHaveLength(4); // back to the 1s base delay, not 4s

		handle.close();
	});

	it('close() stops further reconnection attempts', () => {
		const handle = connectLiveEvents(() => {});
		expect(FakeEventSource.instances).toHaveLength(1);

		handle.close();
		FakeEventSource.instances[0].onerror?.();
		vi.advanceTimersByTime(60_000);

		expect(FakeEventSource.instances).toHaveLength(1);
	});
});

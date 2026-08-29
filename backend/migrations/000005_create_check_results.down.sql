-- Dropping the partitioned parent drops every partition (including the
-- default one and all dated ones created above) as dependent objects.
DROP TABLE IF EXISTS check_results;

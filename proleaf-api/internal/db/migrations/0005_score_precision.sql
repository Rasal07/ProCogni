-- 0005: let a score of 100 be stored.
--
-- NUMERIC(4,2) tops out at 99.99, while the CHECK constraint and the API both
-- allow 100. A full-marks entry therefore failed at the database. Widening is
-- lossless; the 0-100 CHECK on assessments.score is unchanged.

ALTER TABLE assessments ALTER COLUMN score        TYPE NUMERIC(5,2);
ALTER TABLE class_stats ALTER COLUMN mean_score   TYPE NUMERIC(5,2);
ALTER TABLE class_stats ALTER COLUMN median_score TYPE NUMERIC(5,2);
ALTER TABLE class_stats ALTER COLUMN top_score    TYPE NUMERIC(5,2);

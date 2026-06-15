-- 管理后台用户展示名 (DESIGN §16.2)。0001 的 license 表未含 name，这里补充。
-- 方言：PostgreSQL（MySQL 可将 VARCHAR 不变）。
ALTER TABLE license ADD COLUMN name VARCHAR(128) NOT NULL DEFAULT '';

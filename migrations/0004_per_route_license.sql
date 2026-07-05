-- 经济能力查询转接服务 — demo license 治理 (v0.9)
-- 方言：PostgreSQL
-- 背景：历史实现把同一个 demo license（LIC-DEMO-0001 / appKey y89098io、secret 公开
--       于 README）播种进了每个域库（含生产），导致这一个 token 可以访问所有路由。
--       v0.9 起生产启动不再播种 demo；本迁移将旧共享 demo 从所有库中清除。开发/e2e
--       环境由建库脚本（SEED_DEMO=1）按域重新播种各自独立的 demo 凭证
--       (LIC-DEMO-<DOMAIN>，appKey 见 model.DemoAppKey，跨域不可用)。

DELETE FROM quota WHERE license_id = 'LIC-DEMO-0001';
DELETE FROM license WHERE license_id = 'LIC-DEMO-0001';

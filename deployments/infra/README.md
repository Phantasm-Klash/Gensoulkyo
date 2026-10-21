# deployments/infra

基础设施（Redis）的独立 compose 工程。**Postgres 不在这里** —— 它继续由
`gensoulkyo-nakama` 工程持有（容器 `gensoulkyo_nakama_postgres`，
`192.168.32.2:5432`），避免动到已有 volume 与迁移历史。

## 拓扑决策：单实例双库 / 单 Redis 双 db

用户已拍板：**一个 Postgres 里两个 database**（`nakama_prod` 正式 /
`nakama_test` 测试），**一个 Redis 两个逻辑库**（`db0` 正式 / `db1` 测试）。
代价是隔离性弱于双容器，所以本目录与 `runtime/storage` 用三件事兜底：

1. **凭据隔离**：`gensoulkyo_prod` / `gensoulkyo_test` 两个 PG 角色，
   各自只对同名库有 `CONNECT`，并对两个新库 `REVOKE CONNECT FROM PUBLIC`。
   实测 `gensoulkyo_prod -> nakama_test` 与 `gensoulkyo_test -> nakama_prod`
   均被拒绝（`FATAL: permission denied for database`）。
2. **连接串集中配置**：`runtime/storage` 提供 `PostgresConfig(env)` /
   `RedisConfig(env)`，只接受 `prod` / `test` 两种环境，禁止在业务代码里
   手写 DSN。
3. **Redis 需要密码**，且 `db0`/`db1` 只作为命名空间，不作为安全边界。

## Redis

| 项 | 值 |
| --- | --- |
| 容器 | `gensoulkyo_redis`（`redis:7-alpine`） |
| 地址 | `192.168.32.10:6379`（compose 网桥 `gensoulkyo-nakama`，未发布到宿主机） |
| 密码 | `REDIS_PASSWORD`（`deployments/infra/.env`，0600，gitignore） |
| 持久化 | AOF `appendfsync everysec` + RDB `save 900 1`，数据卷 `redis_data` |
| 内存 | `maxmemory 256mb`，`maxmemory-policy noeviction`，容器 `mem_limit 384m` |
| db0 | 正式：session/在线状态、房间实时态、匹配队列、限流、幂等键 |
| db1 | 测试：同上，键前缀与 db0 一致，靠 db index 隔离 |

`noeviction` 的理由：写进 Redis 的都是实时态（房间/票据/幂等键）或安全阀
（限流计数）。静默淘汰一个幂等键会把「重复请求」变成「重复副作用」，比写满时
显式报错更危险；报错可以被监控捕获，静默丢键不能。

## 操作

```sh
cd /root/gotouhou/Gensoulkyo/deployments/infra
cp .env.example .env && chmod 600 .env   # 填入 REDIS_PASSWORD
docker compose up -d
docker compose ps
docker exec gensoulkyo_redis redis-cli --no-auth-warning -a "$REDIS_PASSWORD" -n 0 ping
```

## Postgres 双库

| 库 | 角色 | 内容 |
| --- | --- | --- |
| `nakama_prod` | `gensoulkyo_prod` | 从老库 `nakama` dump/restore 的全量数据 |
| `nakama_test` | `gensoulkyo_test` | 仅 schema（`--schema-only`）+ 迁移标记行，数据为空 |
| `nakama`（老库） | `postgres` | **原样保留**，作为回滚保险，业务代码不再指向它 |

回滚：`/root/gotouhou/backups/<时间戳>/nakama.dump`（`pg_restore -Fc`）。

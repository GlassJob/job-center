-- =====================================================================
-- Job Center  000001_init.sql（未发版合并：配置 + 运行时 + triggers + 日志统计）
-- 目标：MySQL 8.0+（独立库，表名无 job_ 前缀，小写复数）
-- 约定：
--   * DATETIME(3) 存 UTC；连接须 time_zone='+00:00'
--   * 配置实体对外 PK 为 CHAR(36) UUID v7（ascii_bin）
--   * 配置表使用 FK；执行/日志/事件/归档/triggers 不建 FK
--   * 本文件幂等：CREATE IF NOT EXISTS，不含 DROP
-- =====================================================================

SET NAMES utf8mb4;
SET time_zone = '+00:00';
-- ---------------------------------------------------------------------
-- 0. clients  管理端调用方（鉴权用 id=UUID v7；与 service_credentials 分表）
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `clients` (
  `id`             CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7，即 X-JC-Client-Id',
  `name`           VARCHAR(128) NOT NULL COMMENT '展示名，如 GlassAdmin / CI Deploy',
  `description`    VARCHAR(1000) NULL COMMENT '用途说明',
  `secret_hash`    BINARY(32) NOT NULL COMMENT 'HMAC-SHA256(server_pepper, secret)，用于验签比对',
  `secret_enc`     VARBINARY(512) NOT NULL COMMENT 'AES-GCM 加密后的 secret，供 Admin HMAC 验签取回',
  `hash_algo`      VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT 'hmac-sha256-v1' COMMENT '密钥哈希算法标识',
  `secret_hint`    VARCHAR(8) CHARACTER SET ascii NULL COMMENT '明文尾缀提示，便于人工辨认',
  `status`         VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'ACTIVE' COMMENT 'ACTIVE=可用；ROTATING=轮换中；REVOKED=已吊销',
  `scopes`         JSON NOT NULL COMMENT '权限串数组，如 ["job.jobs.view"]；[]=无权限',
  `allowed_ips`    JSON NULL COMMENT '允许来源 IP/CIDR 数组；NULL=不限制',
  `expires_at`     DATETIME(3) NULL COMMENT '过期时间 UTC；NULL=不过期',
  `last_used_at`   DATETIME(3) NULL COMMENT '最近一次成功鉴权时间 UTC',
  `last_used_ip`   VARCHAR(45) CHARACTER SET ascii NULL COMMENT '最近一次成功鉴权来源 IP',
  `created_by`     VARCHAR(128) NULL COMMENT '创建人标识',
  `created_at`     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间 UTC',
  `revoked_at`     DATETIME(3) NULL COMMENT '吊销时间 UTC',
  `revoked_by`     VARCHAR(128) NULL COMMENT '吊销操作人',
  `updated_at`     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '最后更新时间 UTC',
  PRIMARY KEY (`id`),
  KEY `idx_status` (`status`),
  CONSTRAINT `ck_clients_status` CHECK (`status` IN ('ACTIVE','ROTATING','REVOKED')),
  CONSTRAINT `ck_clients_scopes` CHECK (JSON_TYPE(`scopes`) = 'ARRAY'),
  CONSTRAINT `ck_clients_ips` CHECK (`allowed_ips` IS NULL OR JSON_TYPE(`allowed_ips`) = 'ARRAY')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='管理端调用方';

-- ---------------------------------------------------------------------
-- 1. services  逻辑服务
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `services` (
  `id`            CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7',
  `code`          VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '业务编码，^[a-z0-9][a-z0-9._-]{1,63}$，全局唯一',
  `name`          VARCHAR(128) NOT NULL COMMENT '展示名',
  `description`   VARCHAR(1000) NULL COMMENT '服务说明',
  `status`        VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'ACTIVE' COMMENT 'ACTIVE=可用；DISABLED=停用（软禁用）',
  `created_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间 UTC',
  `updated_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '最后更新时间 UTC',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_services_code` (`code`),
  CONSTRAINT `ck_services_status` CHECK (`status` IN ('ACTIVE','DISABLED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='逻辑服务';

-- ---------------------------------------------------------------------
-- 2. service_credentials  Executor/业务 API 凭据（与 clients 分表）
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `service_credentials` (
  `id`                 CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7，公开凭据 ID（非密钥）',
  `service_id`         CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 services.id',
  `name`               VARCHAR(128) NOT NULL COMMENT '展示名，同服务内唯一',
  `secret_hash`        BINARY(32) NOT NULL COMMENT 'HMAC-SHA256(server_pepper, secret)，用于明文凭据比对',
  `secret_enc`         VARBINARY(512) NOT NULL COMMENT 'AES-GCM 加密后的 secret，供 Executor HMAC 验签取回',
  `hash_algo`          VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT 'hmac-sha256-v1' COMMENT '密钥哈希算法标识',
  `secret_hint`        VARCHAR(8) CHARACTER SET ascii NULL COMMENT '明文尾缀提示，便于人工辨认',
  `status`             VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'ACTIVE' COMMENT 'ACTIVE=可用；ROTATING=轮换中；REVOKED=已吊销',
  `allowed_job_codes`  JSON NULL COMMENT '可调用的 job.code 数组，支持前缀通配；NULL=本服务全部 Job',
  `allowed_ips`        JSON NULL COMMENT '允许来源 IP/CIDR 数组；NULL=不限制',
  `expires_at`         DATETIME(3) NULL COMMENT '过期时间 UTC；NULL=不过期',
  `last_used_at`       DATETIME(3) NULL COMMENT '最近一次成功鉴权时间 UTC',
  `last_used_ip`       VARCHAR(45) CHARACTER SET ascii NULL COMMENT '最近一次成功鉴权来源 IP',
  `created_by`         VARCHAR(128) NULL COMMENT '创建人标识',
  `created_at`         DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间 UTC',
  `revoked_at`         DATETIME(3) NULL COMMENT '吊销时间 UTC',
  `revoked_by`         VARCHAR(128) NULL COMMENT '吊销操作人',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_service_cred_name` (`service_id`, `name`),
  KEY `idx_service_status` (`service_id`, `status`),
  CONSTRAINT `fk_cred_service` FOREIGN KEY (`service_id`) REFERENCES `services` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_creds_status` CHECK (`status` IN ('ACTIVE','ROTATING','REVOKED')),
  CONSTRAINT `ck_creds_allowed_json` CHECK (`allowed_job_codes` IS NULL OR JSON_TYPE(`allowed_job_codes`) = 'ARRAY'),
  CONSTRAINT `ck_creds_ips` CHECK (`allowed_ips` IS NULL OR JSON_TYPE(`allowed_ips`) = 'ARRAY')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='服务凭据（Executor/业务 API）';

-- ---------------------------------------------------------------------
-- 3. jobs  作业定义（能力 + Admin 开关）
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `jobs` (
  `id`                        CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7',
  `service_id`                CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 services.id',
  `code`                      VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '同服务内业务编码，对外稳定标识',
  `name`                      VARCHAR(128) NOT NULL COMMENT '展示名',
  `description`               VARCHAR(1000) NULL COMMENT '作业说明',
  `enabled`                   TINYINT(1) NOT NULL DEFAULT 1 COMMENT 'Admin 执行开关：1=允许调度/触发；0=禁止；注册不覆盖',
  `language`                  VARCHAR(16) CHARACTER SET ascii NULL COMMENT '实现语言：go|php|java|python…；Executor 注册前可空',
  `handler`                   VARCHAR(128) CHARACTER SET ascii NULL COMMENT 'Executor 内部执行标识；注册前可空',
  `handler_version`           VARCHAR(32) CHARACTER SET ascii NULL COMMENT '最近一次注册的 handler 版本号',
  `params_schema`             JSON NULL COMMENT '入参 JSON Schema（OBJECT）；NULL=未声明',
  `schema_hash`               CHAR(64) CHARACTER SET ascii NULL COMMENT 'params_schema 内容哈希，用于版本比对',
  `metadata_version`          INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '元数据变更单调版本，每次注册/Admin 改元数据 +1',
  `idempotency_mode`          VARCHAR(20) CHARACTER SET ascii NOT NULL DEFAULT 'UNKNOWN' COMMENT '幂等模式：UNKNOWN=未知；NONE=不幂等；KEYED=按幂等键；NATURAL=自然幂等',
  `idempotency_verified`      TINYINT(1) NOT NULL DEFAULT 0 COMMENT '幂等能力是否经 Admin 确认；仅 Admin 可写，注册不覆盖',
  `supports_cancel`           TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否支持取消执行中任务',
  `supports_timeout`          TINYINT(1) NOT NULL DEFAULT 1 COMMENT '是否支持超时中止',
  `supports_progress`         TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否上报进度',
  `supports_state`            TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否读写 job_states 共享状态',
  `default_timeout_seconds`   INT UNSIGNED NOT NULL DEFAULT 300 COMMENT '默认超时秒数（1～604800）；调度未覆盖时使用',
  `capabilities`              JSON NOT NULL DEFAULT (CAST('{}' AS JSON)) COMMENT '扩展能力 OBJECT，键值由 Executor/协议约定',
  `status`                    VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'ACTIVE' COMMENT 'ACTIVE=正常；ORPHANED=无在线实例；DISABLED=Admin 停用',
  `first_registered_at`       DATETIME(3) NULL COMMENT '首次被 Executor 注册时间 UTC',
  `last_registered_at`        DATETIME(3) NULL COMMENT '最近一次注册时间 UTC',
  `created_at`                DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间 UTC',
  `updated_at`                DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '最后更新时间 UTC',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_service_job` (`service_id`, `code`),
  KEY `idx_code` (`code`),
  KEY `idx_service_enabled` (`service_id`, `enabled`),
  CONSTRAINT `fk_job_service` FOREIGN KEY (`service_id`) REFERENCES `services` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_jobs_status` CHECK (`status` IN ('ACTIVE','ORPHANED','DISABLED')),
  CONSTRAINT `ck_jobs_idem_mode` CHECK (`idempotency_mode` IN ('UNKNOWN','NONE','KEYED','NATURAL')),
  CONSTRAINT `ck_jobs_timeout` CHECK (`default_timeout_seconds` BETWEEN 1 AND 604800),
  CONSTRAINT `ck_jobs_schema_json` CHECK (`params_schema` IS NULL OR JSON_TYPE(`params_schema`) = 'OBJECT'),
  CONSTRAINT `ck_jobs_capabilities_json` CHECK (JSON_TYPE(`capabilities`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='作业定义';

-- ---------------------------------------------------------------------
-- 4. job_versions  元数据/能力版本历史
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `job_versions` (
  `id`                   CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7',
  `job_id`               CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 jobs.id',
  `handler_version`      VARCHAR(32) CHARACTER SET ascii NOT NULL COMMENT '该版本的 handler 完整版本号',
  `handler_major`        INT UNSIGNED NOT NULL COMMENT '主版本号，派发兼容按此对齐',
  `schema_hash`          CHAR(64) CHARACTER SET ascii NOT NULL COMMENT '该版本 params_schema 哈希',
  `params_schema`        JSON NULL COMMENT '该版本入参 JSON Schema（OBJECT）',
  `idempotency_mode`     VARCHAR(20) CHARACTER SET ascii NOT NULL DEFAULT 'UNKNOWN' COMMENT '该版本幂等模式：UNKNOWN|NONE|KEYED|NATURAL',
  `capability_snapshot`  JSON NULL COMMENT 'supports_* 与 capabilities 快照 OBJECT',
  `first_seen_at`        DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '首次观测到该版本时间 UTC',
  `last_seen_at`         DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '最近观测到该版本时间 UTC',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_job_ver` (`job_id`, `handler_version`, `schema_hash`),
  KEY `idx_job_major` (`job_id`, `handler_major`, `id`),
  CONSTRAINT `fk_jobver_job` FOREIGN KEY (`job_id`) REFERENCES `jobs` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_jobver_idem_mode` CHECK (`idempotency_mode` IN ('UNKNOWN','NONE','KEYED','NATURAL')),
  CONSTRAINT `ck_jobver_schema_json` CHECK (`params_schema` IS NULL OR JSON_TYPE(`params_schema`) = 'OBJECT'),
  CONSTRAINT `ck_jobver_cap_json` CHECK (`capability_snapshot` IS NULL OR JSON_TYPE(`capability_snapshot`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='作业元数据版本历史';

-- ---------------------------------------------------------------------
-- 5. schedules  Job 1:N 调度计划
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `schedules` (
  `id`                          CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7',
  `job_id`                      CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 jobs.id',
  `code`                        VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '同 Job 内调度编码，对外稳定标识',
  `name`                        VARCHAR(128) NOT NULL COMMENT '展示名',
  `enabled`                     TINYINT(1) NOT NULL DEFAULT 0 COMMENT 'Cron 自动调度开关：1=按 cron 触发；0=仅可手动（取决于 manual_enabled）',
  `manual_enabled`              TINYINT(1) NOT NULL DEFAULT 1 COMMENT '是否允许按本计划手动触发：1=允许；0=禁止',
  `cron_expression`             VARCHAR(128) CHARACTER SET ascii NULL COMMENT '6 字段 Cron：秒 分 时 日 月 周；enabled=1 时必填',
  `timezone`                    VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT 'UTC' COMMENT 'Cron 解释时区，IANA 名如 Asia/Shanghai',
  `default_params`              JSON NULL COMMENT '触发时默认入参 OBJECT；与手动/触发入参合并',
  `timeout_seconds`             INT UNSIGNED NOT NULL DEFAULT 300 COMMENT '本计划执行超时秒数（1～604800），覆盖 Job 默认',
  `max_retries`                 INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '平台 Attempt 失败后最大重试次数（不含首次）',
  `retry_backoff`               JSON NOT NULL DEFAULT (CAST('{"type":"fixed","interval_seconds":60}' AS JSON)) COMMENT '重试退避策略 OBJECT，如 {"type":"fixed","interval_seconds":60}',
  `concurrency_policy`          VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'FORBID' COMMENT '撞并发上限时：ALLOW=仍开跑；FORBID=拒绝/跳过；QUEUE=入队等待',
  `concurrency_scope`           VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'JOB' COMMENT '并发计数范围：JOB=整 Job；SCHEDULE=仅本计划；IDEMPOTENCY_KEY=同一幂等键',
  `max_concurrency`             INT UNSIGNED NOT NULL DEFAULT 1 COMMENT 'concurrency_scope 内允许同时运行的最大执行数',
  `queue_max`                   INT UNSIGNED NOT NULL DEFAULT 10 COMMENT 'policy=QUEUE 时等待队列上限；满则丢弃/拒绝新触发',
  `misfire_policy`              VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'SKIP' COMMENT '错过触发点：SKIP=跳过；FIRE_ONCE=补跑一次；CATCH_UP=按 catch_up_max 追赶',
  `misfire_threshold_seconds`   INT UNSIGNED NOT NULL DEFAULT 60 COMMENT '超过该秒数未触发才视为 misfire',
  `catch_up_max`                INT UNSIGNED NOT NULL DEFAULT 10 COMMENT 'misfire_policy=CATCH_UP 时最多补跑次数（1～100）',
  `dispatch_strategy`           VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'LEAST_LOAD' COMMENT '选实例策略：LEAST_LOAD=最少负载；ROUND_ROBIN=轮询；RANDOM=随机',
  `next_fire_at`                DATETIME(3) NULL COMMENT '下次计划触发时间 UTC；调度器扫描用',
  `last_fire_at`                DATETIME(3) NULL COMMENT '上次实际触发时间 UTC',
  `horizon_at`                   DATETIME(3) NULL COMMENT '调度器已预生成 triggers 到此时刻 UTC',
  `config_source`               VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'DEFAULT' COMMENT '配置来源：DEFAULT=系统默认；ADMIN=人工改过',
  `version`                     INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '乐观锁版本；更新须带 If-Match / 期望 version',
  `updated_by`                  VARCHAR(128) NULL COMMENT '最近修改人标识',
  `created_at`                  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间 UTC',
  `updated_at`                  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '最后更新时间 UTC',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_job_schedule` (`job_id`, `code`),
  KEY `idx_due` (`enabled`, `next_fire_at`),
  CONSTRAINT `fk_schedule_job` FOREIGN KEY (`job_id`) REFERENCES `jobs` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_sch_cron` CHECK (`enabled` = 0 OR `cron_expression` IS NOT NULL),
  CONSTRAINT `ck_sch_timeout` CHECK (`timeout_seconds` BETWEEN 1 AND 604800),
  CONSTRAINT `ck_sch_conc` CHECK (`concurrency_policy` IN ('ALLOW','FORBID','QUEUE')),
  CONSTRAINT `ck_sch_scope` CHECK (`concurrency_scope` IN ('JOB','SCHEDULE','IDEMPOTENCY_KEY')),
  CONSTRAINT `ck_sch_misfire` CHECK (`misfire_policy` IN ('SKIP','FIRE_ONCE','CATCH_UP')),
  CONSTRAINT `ck_sch_catchup` CHECK (`catch_up_max` BETWEEN 1 AND 100),
  CONSTRAINT `ck_sch_dispatch` CHECK (`dispatch_strategy` IN ('LEAST_LOAD','ROUND_ROBIN','RANDOM')),
  CONSTRAINT `ck_sch_source` CHECK (`config_source` IN ('DEFAULT','ADMIN')),
  CONSTRAINT `ck_sch_params_json` CHECK (`default_params` IS NULL OR JSON_TYPE(`default_params`) = 'OBJECT'),
  CONSTRAINT `ck_sch_retry_json` CHECK (JSON_TYPE(`retry_backoff`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='调度计划';

-- ---------------------------------------------------------------------
-- 6. job_states  多业务键共享状态
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `job_states` (
  `id`                        CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7',
  `job_id`                    CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 jobs.id',
  `key`                       VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '业务状态键，同 Job 内唯一，如 shop:us_001:incremental',
  `value`                     JSON NOT NULL COMMENT '状态值 OBJECT；应用层建议 ≤64KiB',
  `version`                   BIGINT UNSIGNED NOT NULL DEFAULT 1 COMMENT 'CAS 版本；写时须匹配期望值；逻辑空视图为 0',
  `updated_by`                VARCHAR(128) NOT NULL COMMENT '最近写入方，如 attempt:… / admin:… / execution:…',
  `updated_by_execution_id`   CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '若由执行写入，对应 executions.id',
  `updated_at`                DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '最后更新时间 UTC',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_job_state_key` (`job_id`, `key`),
  CONSTRAINT `fk_job_state_job` FOREIGN KEY (`job_id`) REFERENCES `jobs` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_job_state_json` CHECK (JSON_TYPE(`value`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='作业共享状态';

-- ---------------------------------------------------------------------
-- 7. operation_audits  管理操作审计（client_id 必填，无默认 glassadmin）
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `operation_audits` (
  `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '自增主键',
  `client_id`       CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '已认证 clients.id（UUID v7）',
  `operator_id`     VARCHAR(64) NULL COMMENT '操作人 ID（上游透传）',
  `operator_name`   VARCHAR(128) NULL COMMENT '操作人显示名',
  `action`          VARCHAR(64) CHARACTER SET ascii NOT NULL COMMENT '动作名，如 create_job / patch_schedule',
  `permission`      VARCHAR(64) CHARACTER SET ascii NULL COMMENT '校验用到的权限串，如 job.schedules.manage',
  `resource_type`   VARCHAR(32) CHARACTER SET ascii NOT NULL COMMENT '资源类型，如 job / schedule / credential',
  `resource_id`     VARCHAR(128) NULL COMMENT '资源主键或业务编码',
  `request_id`      VARCHAR(64) CHARACTER SET ascii NULL COMMENT '请求 ID，便于与网关日志关联',
  `trace_id`        VARCHAR(64) CHARACTER SET ascii NULL COMMENT '分布式追踪 ID',
  `client_ip`       VARCHAR(45) CHARACTER SET ascii NULL COMMENT '调用方 IP',
  `before_data`     JSON NULL COMMENT '变更前快照 OBJECT/ARRAY',
  `after_data`      JSON NULL COMMENT '变更后快照 OBJECT/ARRAY',
  `result`          VARCHAR(16) CHARACTER SET ascii NOT NULL COMMENT 'SUCCESS=成功；DENIED=无权限；FAILED=执行失败',
  `error_message`   VARCHAR(1000) NULL COMMENT '失败/拒绝原因摘要',
  `created_at`      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '审计写入时间 UTC',
  PRIMARY KEY (`id`),
  KEY `idx_client_created` (`client_id`, `created_at`),
  KEY `idx_client_operator_created` (`client_id`, `operator_id`, `created_at`),
  KEY `idx_resource` (`resource_type`, `resource_id`),
  KEY `idx_created` (`created_at`),
  CONSTRAINT `ck_audit_result` CHECK (`result` IN ('SUCCESS','DENIED','FAILED')),
  CONSTRAINT `ck_audit_before_json` CHECK (`before_data` IS NULL OR JSON_TYPE(`before_data`) IN ('OBJECT','ARRAY')),
  CONSTRAINT `ck_audit_after_json` CHECK (`after_data` IS NULL OR JSON_TYPE(`after_data`) IN ('OBJECT','ARRAY'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='管理操作审计';

-- ---------------------------------------------------------------------
-- B1. instances  Executor 运行实例
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `instances` (
  `id`                CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7',
  `service_id`        CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 services.id',
  `instance_key`      VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'Executor 实例稳定标识，进程重启可复用',
  `session_id`        VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '当前长连接会话 ID；重连后替换',
  `session_epoch`     BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '每次成功认证 +1，用于作废旧连接',
  `environment`       VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT 'dev' COMMENT '运行环境，如 dev|staging|prod',
  `hostname`          VARCHAR(255) NOT NULL COMMENT '主机名（自报）',
  `ip_address`        VARCHAR(45) CHARACTER SET ascii NULL COMMENT '中心观测到的对端 IP',
  `reported_ip`       VARCHAR(45) CHARACTER SET ascii NULL COMMENT 'Executor 自报 IP',
  `pid`               INT UNSIGNED NULL COMMENT '进程 PID（自报）',
  `language`          VARCHAR(16) CHARACTER SET ascii NOT NULL COMMENT '运行时语言：go|php|java|python…',
  `runtime_version`   VARCHAR(64) NULL COMMENT '语言运行时版本，如 go1.22.1',
  `sdk_version`       VARCHAR(32) NULL COMMENT 'Job Center SDK 版本',
  `protocol_version`  VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT '1.0' COMMENT '与中心通信协议版本',
  `app_version`       VARCHAR(64) NULL COMMENT '业务应用版本号',
  `git_commit`        VARCHAR(64) CHARACTER SET ascii NULL COMMENT '构建对应 git commit',
  `build_time`        DATETIME(3) NULL COMMENT '构建时间 UTC',
  `started_at`        DATETIME(3) NOT NULL COMMENT '进程启动时间 UTC（自报）',
  `registered_at`     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '首次注册到中心时间 UTC',
  `connected_at`      DATETIME(3) NULL COMMENT '当前会话建立时间 UTC',
  `disconnected_at`   DATETIME(3) NULL COMMENT '最近断开时间 UTC',
  `last_heartbeat_at` DATETIME(3) NULL COMMENT '最近心跳时间 UTC',
  `status`            VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'REGISTERING' COMMENT 'REGISTERING=注册中；ONLINE=在线；SUSPECT=心跳可疑；OFFLINE=离线；DRAINING=排水下线',
  `max_concurrency`   INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '该实例全局同时运行 Attempt 上限',
  `running_count`     INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '当前运行中数量（展示用）；容量以 execution_attempts 为准',
  `gateway_node_id`   VARCHAR(64) CHARACTER SET ascii NULL COMMENT '接入的网关/中心节点 ID',
  `connection_id`     VARCHAR(64) CHARACTER SET ascii NULL COMMENT '连接诊断 ID，仅排障用',
  `metadata`          JSON NULL COMMENT '扩展元数据 OBJECT',
  `created_at`        DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间 UTC',
  `updated_at`        DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '最后更新时间 UTC',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_instance_key` (`instance_key`),
  KEY `idx_service_status` (`service_id`, `status`),
  KEY `idx_status_heartbeat` (`status`, `last_heartbeat_at`),
  KEY `idx_service_commit` (`service_id`, `git_commit`),
  CONSTRAINT `fk_instance_service` FOREIGN KEY (`service_id`) REFERENCES `services` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_instance_status` CHECK (`status` IN ('REGISTERING','ONLINE','SUSPECT','OFFLINE','DRAINING')),
  CONSTRAINT `ck_instance_meta_json` CHECK (`metadata` IS NULL OR JSON_TYPE(`metadata`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='Executor 运行实例';

-- ---------------------------------------------------------------------
-- B2. instance_jobs  实例当前提供的 Job 能力
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `instance_jobs` (
  `id`              CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7',
  `instance_id`     CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 instances.id',
  `job_id`          CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '可执行的 jobs.id',
  `handler_version` VARCHAR(32) CHARACTER SET ascii NOT NULL COMMENT '该实例上此 Job 的 handler 版本',
  `handler_major`   INT UNSIGNED NOT NULL COMMENT '主版本号；派发时须与 executions.handler_major 兼容',
  `schema_hash`     CHAR(64) CHARACTER SET ascii NOT NULL COMMENT '该实例上报的 params_schema 哈希',
  `max_concurrency` INT UNSIGNED NULL COMMENT '该实例上此 Job 并发上限；NULL=不单独限制',
  `registered_at`   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '该能力登记时间 UTC',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_instance_job` (`instance_id`, `job_id`),
  KEY `idx_job_major` (`job_id`, `handler_major`),
  CONSTRAINT `fk_ij_instance` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON DELETE CASCADE ON UPDATE RESTRICT,
  CONSTRAINT `fk_ij_job` FOREIGN KEY (`job_id`) REFERENCES `jobs` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='实例 Job 能力';

-- ---------------------------------------------------------------------
-- B3. deployments  发布记录与期望版本
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `deployments` (
  `id`                 CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7',
  `service_id`         CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 services.id',
  `environment`        VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT 'dev' COMMENT '目标环境，如 dev|staging|prod',
  `deployment_version` VARCHAR(64) NOT NULL COMMENT '发布版本号（业务侧约定）',
  `git_commit`         VARCHAR(64) CHARACTER SET ascii NOT NULL COMMENT '本次发布对应 git commit',
  `build_id`           VARCHAR(128) CHARACTER SET ascii NOT NULL COMMENT 'CI/CD build 标识，同服务同环境唯一',
  `build_url`          VARCHAR(500) NULL COMMENT '构建产物或流水线链接',
  `deployment_status`  VARCHAR(16) CHARACTER SET ascii NOT NULL COMMENT 'PENDING=待发布；DEPLOYING=发布中；SUCCEEDED=成功；FAILED=失败；ROLLED_BACK=已回滚',
  `expected_key`       VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '当前期望版本标记：= service_id:environment；非当前期望则为 NULL',
  `source`             VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'API' COMMENT '来源：API=接口上报；MANUAL=人工录入',
  `triggered_by`       VARCHAR(128) NULL COMMENT '触发人/系统标识',
  `payload`            JSON NULL COMMENT '发布附加信息 OBJECT',
  `deployed_at`        DATETIME(3) NULL COMMENT '实际部署完成时间 UTC',
  `created_at`         DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间 UTC',
  `updated_at`         DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '最后更新时间 UTC',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_build` (`service_id`, `environment`, `build_id`),
  UNIQUE KEY `uk_expected` (`expected_key`),
  KEY `idx_service_env_created` (`service_id`, `environment`, `created_at`),
  CONSTRAINT `fk_deploy_service` FOREIGN KEY (`service_id`) REFERENCES `services` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_deploy_status` CHECK (`deployment_status` IN ('PENDING','DEPLOYING','SUCCEEDED','FAILED','ROLLED_BACK')),
  CONSTRAINT `ck_deploy_source` CHECK (`source` IN ('API','MANUAL'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='发布记录与期望版本';

-- ---------------------------------------------------------------------
-- C1. executions  逻辑执行（无 FK；可空 schedule_id 的 UNIQUE 允许多个 NULL）
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `executions` (
  `id`                   CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7',
  `service_id`           CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属服务 ID（冗余，便于按服务查询）',
  `job_id`               CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 jobs.id',
  `job_code`             VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '创建时 job.code 快照，避免后续改码影响历史',
  `schedule_id`          CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '来源 schedules.id；手动/API 触发可为空',
  `trigger_type`         VARCHAR(16) CHARACTER SET ascii NOT NULL COMMENT '触发方式：CRON|MANUAL|API|CATCH_UP|SYSTEM',
  `triggered_by`         VARCHAR(128) NOT NULL COMMENT '触发者：操作人 / client_id / system 组件名',
  `trigger_context`      JSON NULL COMMENT '触发上下文 OBJECT（请求头、来源节点等）',
  `params`               JSON NULL COMMENT '实际入参 OBJECT（已与 default_params 合并）',
  `policy_snapshot`      JSON NOT NULL COMMENT '创建时策略快照 OBJECT（超时/重试/并发等，执行期不变）',
  `idempotency_key`      VARCHAR(128) NULL COMMENT '幂等键；同 job_id 下唯一，用于去重',
  `status`               VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'PENDING' COMMENT 'PENDING|QUEUED|DISPATCHING|RUNNING|RETRY_WAIT|SUCCEEDED|FAILED|CANCELLED|TIMED_OUT|SKIPPED|UNKNOWN',
  `status_reason`        VARCHAR(128) NULL COMMENT '状态原因：短码或人工说明（如 ADMIN_CANCEL / 取消备注）',
  `handler_version`      VARCHAR(32) CHARACTER SET ascii NULL COMMENT '选定/实际 handler 版本',
  `handler_major`        INT UNSIGNED NULL COMMENT '选定主版本，派发兼容用',
  `schema_hash`          CHAR(64) CHARACTER SET ascii NULL COMMENT '创建时 params_schema 哈希快照',
  `timeout_seconds`      INT UNSIGNED NOT NULL COMMENT '本执行超时秒数（来自 schedule/job 策略）',
  `max_attempts`         INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '允许的最大 Attempt 次数（含首次）',
  `attempt_count`        INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '已创建的 Attempt 数量',
  `current_attempt_no`   INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '当前 Attempt 序号；无则为 0',
  `current_attempt_id`   CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '当前 execution_attempts.id',
  `concurrency_slot`     VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '非终态并发占位键；终态置 NULL 以释放槽位',
  `next_attempt_at`      DATETIME(3) NULL COMMENT '下次可派发/重试时间 UTC；调度扫描用',
  `deadline_at`          DATETIME(3) NULL COMMENT '整次执行截止时间 UTC',
  `cancel_requested_at`  DATETIME(3) NULL COMMENT '取消请求时间 UTC',
  `cancel_requested_by`  VARCHAR(128) NULL COMMENT '取消请求人/系统',
  `result`               JSON NULL COMMENT '终态结果 OBJECT；建议 ≤64KB',
  `result_summary`       VARCHAR(500) NULL COMMENT '结果摘要，列表展示用',
  `error_code`           VARCHAR(64) CHARACTER SET ascii NULL COMMENT '终态错误码',
  `error_message`        VARCHAR(2000) NULL COMMENT '终态错误信息',
  `scheduled_at`         DATETIME(3) NULL COMMENT 'Cron 计划触发点；手动多为请求时间',
  `queued_at`            DATETIME(3) NULL COMMENT '进入排队时间 UTC',
  `started_at`           DATETIME(3) NULL COMMENT '首次开始执行时间 UTC',
  `finished_at`          DATETIME(3) NULL COMMENT '进入终态时间 UTC',
  `version`              INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '乐观锁版本',
  `created_at`           DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间 UTC',
  `updated_at`           DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '最后更新时间 UTC',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_schedule_tick` (`schedule_id`, `scheduled_at`),
  UNIQUE KEY `uk_job_idem` (`job_id`, `idempotency_key`),
  UNIQUE KEY `uk_concurrency_slot` (`concurrency_slot`),
  KEY `idx_dispatch` (`status`, `next_attempt_at`),
  KEY `idx_job_created` (`job_id`, `created_at`),
  KEY `idx_job_status` (`job_id`, `status`),
  KEY `idx_service_status` (`service_id`, `status`),
  KEY `idx_created` (`created_at`),
  CONSTRAINT `ck_exec_status` CHECK (`status` IN (
    'PENDING','QUEUED','DISPATCHING','RUNNING','RETRY_WAIT',
    'SUCCEEDED','FAILED','CANCELLED','TIMED_OUT','SKIPPED','UNKNOWN'
  )),
  CONSTRAINT `ck_exec_trigger` CHECK (`trigger_type` IN ('CRON','MANUAL','API','CATCH_UP','SYSTEM')),
  CONSTRAINT `ck_exec_params_json` CHECK (`params` IS NULL OR JSON_TYPE(`params`) = 'OBJECT'),
  CONSTRAINT `ck_exec_policy_json` CHECK (JSON_TYPE(`policy_snapshot`) = 'OBJECT'),
  CONSTRAINT `ck_exec_trigger_ctx` CHECK (`trigger_context` IS NULL OR JSON_TYPE(`trigger_context`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='逻辑执行';

-- ---------------------------------------------------------------------
-- C2. execution_attempts  实际派发尝试（无 FK）
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `execution_attempts` (
  `id`                  CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7',
  `execution_id`        CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 executions.id',
  `attempt_no`          INT UNSIGNED NOT NULL COMMENT '第几次尝试，从 1 开始',
  `job_id`              CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '冗余 jobs.id',
  `service_id`          CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '冗余 services.id',
  `instance_id`         CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '派发目标 instances.id',
  `instance_key`        VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '派发时 instance_key 快照',
  `hostname`            VARCHAR(255) NULL COMMENT '派发时主机名快照',
  `git_commit`          VARCHAR(64) CHARACTER SET ascii NULL COMMENT '派发时 git commit 快照',
  `app_version`         VARCHAR(64) NULL COMMENT '派发时应用版本快照',
  `handler_version`     VARCHAR(32) CHARACTER SET ascii NULL COMMENT '实际使用的 handler 版本',
  `lease_token`         CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '执行租约令牌（128bit hex），续约/回收用',
  `lease_expires_at`    DATETIME(3) NULL COMMENT '租约过期时间 UTC；超时可判定 LOST',
  `last_heartbeat_at`   DATETIME(3) NULL COMMENT '该 Attempt 最近心跳时间 UTC',
  `status`              VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'CREATED' COMMENT 'CREATED|DISPATCHING|ACKNOWLEDGED|RUNNING|SUCCEEDED|FAILED|CANCELLED|TIMED_OUT|LOST',
  `status_reason`       VARCHAR(128) NULL COMMENT '状态原因：短码或人工说明',
  `dispatch_count`      INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '向 Executor 派发/重派次数',
  `dispatched_at`       DATETIME(3) NULL COMMENT '最近一次派发时间 UTC',
  `acked_at`            DATETIME(3) NULL COMMENT 'Executor 确认收到时间 UTC',
  `started_at`          DATETIME(3) NULL COMMENT 'Executor 开始执行时间 UTC',
  `finished_at`         DATETIME(3) NULL COMMENT '进入终态时间 UTC',
  `deadline_at`         DATETIME(3) NULL COMMENT '本 Attempt 截止时间 UTC',
  `duration_ms`         BIGINT UNSIGNED NULL COMMENT '执行耗时毫秒',
  `progress_percent`    DECIMAL(5,2) NULL COMMENT '进度百分比 0～100',
  `progress_message`    VARCHAR(500) NULL COMMENT '进度说明文案',
  `progress_updated_at` DATETIME(3) NULL COMMENT '进度最近更新时间 UTC',
  `cancel_state`        VARCHAR(24) CHARACTER SET ascii NOT NULL DEFAULT 'NONE' COMMENT 'NONE|REQUESTED|SENT|ACKED|STOPPED|FAILED|CANCEL_UNCONFIRMED',
  `cancel_requested_at` DATETIME(3) NULL COMMENT '取消请求时间 UTC',
  `cancel_acked_at`     DATETIME(3) NULL COMMENT 'Executor 确认取消时间 UTC',
  `exit_code`           INT NULL COMMENT '进程/任务退出码（若有）',
  `error_code`          VARCHAR(64) CHARACTER SET ascii NULL COMMENT '失败错误码',
  `error_message`       VARCHAR(2000) NULL COMMENT '失败错误信息',
  `error_detail`        TEXT NULL COMMENT '失败详情（堆栈等，可截断）',
  `result`              JSON NULL COMMENT '本 Attempt 结果 OBJECT',
  `last_log_seq`        BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '已接收的最大 execution_logs.sequence',
  `log_info_collected`  INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '实际入库 INFO（含 DEBUG）条数',
  `log_warn_collected`  INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '实际入库 WARN 条数',
  `log_error_collected` INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '实际入库 ERROR 条数',
  `log_info_dropped`    INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '缓冲丢弃 INFO（含 DEBUG）累计',
  `log_warn_dropped`    INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '缓冲丢弃 WARN 累计',
  `log_error_dropped`   INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '缓冲丢弃 ERROR 累计',
  `log_warn_count`      INT UNSIGNED GENERATED ALWAYS AS (`log_warn_collected` + `log_warn_dropped`) VIRTUAL COMMENT 'WARN 总量 = 入库 + 丢弃',
  `log_error_count`     INT UNSIGNED GENERATED ALWAYS AS (`log_error_collected` + `log_error_dropped`) VIRTUAL COMMENT 'ERROR 总量 = 入库 + 丢弃',
  `lost_at`             DATETIME(3) NULL COMMENT '判定为 LOST 的时间 UTC',
  `version`             INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '乐观锁版本',
  `created_at`          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间 UTC',
  `updated_at`          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '最后更新时间 UTC',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_exec_attempt_no` (`execution_id`, `attempt_no`),
  KEY `idx_status_lease` (`status`, `lease_expires_at`),
  KEY `idx_status_deadline` (`status`, `deadline_at`),
  KEY `idx_instance_status` (`instance_id`, `status`),
  KEY `idx_job_created` (`job_id`, `created_at`),
  KEY `idx_created` (`created_at`),
  CONSTRAINT `ck_att_status` CHECK (`status` IN (
    'CREATED','DISPATCHING','ACKNOWLEDGED','RUNNING','SUCCEEDED','FAILED','CANCELLED','TIMED_OUT','LOST'
  )),
  CONSTRAINT `ck_att_cancel` CHECK (`cancel_state` IN (
    'NONE','REQUESTED','SENT','ACKED','STOPPED','FAILED','CANCEL_UNCONFIRMED'
  )),
  CONSTRAINT `ck_att_progress` CHECK (`progress_percent` IS NULL OR (`progress_percent` >= 0 AND `progress_percent` <= 100))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='执行尝试';

-- ---------------------------------------------------------------------
-- C3. execution_events  状态事件（按月分区；PK 含分区列）
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `execution_events` (
  `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '分区内自增 ID',
  `execution_id`  CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 executions.id',
  `attempt_id`    CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '关联 execution_attempts.id；与 Attempt 无关时可空',
  `event_type`    VARCHAR(32) CHARACTER SET ascii NOT NULL COMMENT '事件类型，如 status_changed / cancel_requested / lease_renewed',
  `from_status`   VARCHAR(16) CHARACTER SET ascii NULL COMMENT '变更前状态',
  `to_status`     VARCHAR(16) CHARACTER SET ascii NULL COMMENT '变更后状态',
  `actor_type`    VARCHAR(16) CHARACTER SET ascii NOT NULL COMMENT '动作发起方类型：SYSTEM|ADMIN|EXECUTOR|API',
  `actor_id`      VARCHAR(128) NULL COMMENT '发起方标识（节点/操作人/凭据等）',
  `instance_id`   CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '相关 instances.id',
  `payload`       JSON NULL COMMENT '事件附加数据 OBJECT',
  `trace_id`      VARCHAR(64) CHARACTER SET ascii NULL COMMENT '分布式追踪 ID',
  `request_id`    VARCHAR(64) CHARACTER SET ascii NULL COMMENT '请求 ID',
  `created_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '事件时间 UTC；亦为分区键',
  PRIMARY KEY (`id`, `created_at`),
  KEY `idx_exec_id` (`execution_id`, `id`),
  KEY `idx_type_created` (`event_type`, `created_at`),
  KEY `idx_created` (`created_at`),
  CONSTRAINT `ck_evt_actor` CHECK (`actor_type` IN ('SYSTEM','ADMIN','EXECUTOR','API')),
  CONSTRAINT `ck_evt_payload` CHECK (`payload` IS NULL OR JSON_TYPE(`payload`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='执行事件'
PARTITION BY RANGE COLUMNS(`created_at`) (
  PARTITION p202611 VALUES LESS THAN ('2026-12-01 00:00:00'),
  PARTITION p202612 VALUES LESS THAN ('2027-01-01 00:00:00'),
  PARTITION p202701 VALUES LESS THAN ('2027-02-01 00:00:00'),
  PARTITION p202702 VALUES LESS THAN ('2027-03-01 00:00:00'),
  PARTITION p202703 VALUES LESS THAN ('2027-04-01 00:00:00'),
  PARTITION pmax    VALUES LESS THAN (MAXVALUE)
);

-- ---------------------------------------------------------------------
-- C4. node_leases  内部组件选主
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `node_leases` (
  `lease_name`     VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '租约名：scheduler|lease-sweeper|archiver 等',
  `owner_node_id`  VARCHAR(64) CHARACTER SET ascii NOT NULL COMMENT '当前持有该租约的节点 ID',
  `fencing_token`  BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '每次换主 +1，用于 fencing 防脑裂',
  `lease_until`    DATETIME(3) NOT NULL COMMENT '租约有效期截止 UTC；过期可被抢占',
  `updated_at`     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '最近续约/换主时间 UTC',
  PRIMARY KEY (`lease_name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='节点租约';

-- ---------------------------------------------------------------------
-- D1. execution_logs  执行日志（按月分区）
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `execution_logs` (
  `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '分区内自增 ID',
  `execution_id`  CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 executions.id',
  `attempt_id`    CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 execution_attempts.id',
  `sequence`      BIGINT UNSIGNED NOT NULL COMMENT 'Executor 端按 attempt 从 1 递增的序号',
  `level`         VARCHAR(8) CHARACTER SET ascii NOT NULL COMMENT '日志级别：DEBUG|INFO|WARN|ERROR',
  `stream`        VARCHAR(8) CHARACTER SET ascii NULL COMMENT '来源流：stdout|stderr；结构化日志为 NULL',
  `message`       TEXT NOT NULL COMMENT '已脱敏正文，单条建议 ≤8KB',
  `fields`        JSON NULL COMMENT '结构化字段 OBJECT',
  `truncated`     TINYINT(1) NOT NULL DEFAULT 0 COMMENT '1=因超长被截断',
  `logged_at`     DATETIME(3) NOT NULL COMMENT 'Executor 侧日志时间 UTC',
  `created_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '中心入库时间 UTC；亦为分区键',
  PRIMARY KEY (`id`, `created_at`),
  KEY `idx_attempt_seq` (`attempt_id`, `sequence`),
  KEY `idx_exec_attempt_seq` (`execution_id`, `attempt_id`, `sequence`),
  KEY `idx_created` (`created_at`),
  CONSTRAINT `ck_log_level` CHECK (`level` IN ('DEBUG','INFO','WARN','ERROR')),
  CONSTRAINT `ck_log_fields_json` CHECK (`fields` IS NULL OR JSON_TYPE(`fields`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='执行日志'
PARTITION BY RANGE COLUMNS(`created_at`) (
  PARTITION p202611 VALUES LESS THAN ('2026-12-01 00:00:00'),
  PARTITION p202612 VALUES LESS THAN ('2027-01-01 00:00:00'),
  PARTITION p202701 VALUES LESS THAN ('2027-02-01 00:00:00'),
  PARTITION p202702 VALUES LESS THAN ('2027-03-01 00:00:00'),
  PARTITION p202703 VALUES LESS THAN ('2027-04-01 00:00:00'),
  PARTITION pmax    VALUES LESS THAN (MAXVALUE)
);

-- ---------------------------------------------------------------------
-- D2. archive_runs  归档作业记录
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `archive_runs` (
  `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '自增主键',
  `table_name`    VARCHAR(64) CHARACTER SET ascii NOT NULL COMMENT '被归档的表名，如 execution_logs',
  `archive_date`  DATE NOT NULL COMMENT '归档覆盖的 UTC 自然日',
  `status`        VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'RUNNING' COMMENT 'RUNNING=进行中；EXPORTED=已导出未删；SUCCEEDED=完成；FAILED=失败',
  `file_uri`      VARCHAR(500) NULL COMMENT '归档文件 URI（对象存储路径等）',
  `row_count`     BIGINT UNSIGNED NULL COMMENT '导出行数',
  `file_bytes`    BIGINT UNSIGNED NULL COMMENT '归档文件字节数',
  `checksum`      CHAR(64) CHARACTER SET ascii NULL COMMENT '归档文件校验和（如 SHA-256 hex）',
  `deleted_rows`  BIGINT UNSIGNED NULL COMMENT '从热表删除的行数',
  `error_message` VARCHAR(2000) NULL COMMENT '失败原因',
  `attempts`      INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '本归档任务尝试次数',
  `started_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '开始时间 UTC',
  `exported_at`   DATETIME(3) NULL COMMENT '导出完成时间 UTC',
  `finished_at`   DATETIME(3) NULL COMMENT '整次归档结束时间 UTC',
  `updated_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '最后更新时间 UTC',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_table_date` (`table_name`, `archive_date`),
  KEY `idx_status_date` (`status`, `archive_date`),
  CONSTRAINT `ck_arch_status` CHECK (`status` IN ('RUNNING','EXPORTED','SUCCEEDED','FAILED')),
  CONSTRAINT `ck_arch_delete_after_export` CHECK (`status` <> 'SUCCEEDED' OR (`file_uri` IS NOT NULL AND `checksum` IS NOT NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='归档执行记录';

-- ---------------------------------------------------------------------
-- E1. schedule_triggers  Cron 预生成开火点
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `schedule_triggers` (
  `id`            CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7',
  `schedule_id`   CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'schedules.id',
  `job_id`        CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'jobs.id 冗余',
  `fire_at`       DATETIME(3) NOT NULL COMMENT '计划开火时间 UTC',
  `status`        VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'PENDING' COMMENT 'PENDING|CLAIMED|FIRED|SKIPPED|CANCELLED',
  `claimed_by`    VARCHAR(64) CHARACTER SET ascii NULL COMMENT '认领节点 node_id',
  `claimed_at`    DATETIME(3) NULL COMMENT '认领时间 UTC',
  `claim_until`   DATETIME(3) NULL COMMENT '认领租约截止 UTC',
  `execution_id`  CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT 'FIRED 后回写 executions.id',
  `skip_reason`   VARCHAR(128) NULL COMMENT 'SKIPPED/CANCELLED 原因',
  `created_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_schedule_fire` (`schedule_id`, `fire_at`),
  KEY `idx_due` (`status`, `fire_at`),
  KEY `idx_job` (`job_id`, `fire_at`),
  CONSTRAINT `ck_trig_status` CHECK (`status` IN ('PENDING','CLAIMED','FIRED','SKIPPED','CANCELLED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='Cron 预生成触发点';

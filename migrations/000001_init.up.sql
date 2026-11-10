-- =====================================================================
-- Job Center V2  Phase A  0001_init.sql
-- 目标：MySQL 8.0+（独立库，表名无 job_ 前缀，小写复数）
-- 约定：
--   * DATETIME(3) 存 UTC；连接须 time_zone='+00:00'
--   * 配置实体对外 PK 为 CHAR(36) UUID v7（ascii_bin）
--   * 配置表使用 FK；审计表不建 FK
--   * 本文件幂等：CREATE IF NOT EXISTS / INSERT IGNORE，不含 DROP
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
  `service_code`  VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '业务编码，^[a-z0-9][a-z0-9._-]{1,63}$，全局唯一',
  `name`          VARCHAR(128) NOT NULL COMMENT '展示名',
  `description`   VARCHAR(1000) NULL COMMENT '服务说明',
  `status`        VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'ACTIVE' COMMENT 'ACTIVE=可用；DISABLED=停用（软禁用）',
  `created_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间 UTC',
  `updated_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '最后更新时间 UTC',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_service_code` (`service_code`),
  CONSTRAINT `ck_services_status` CHECK (`status` IN ('ACTIVE','DISABLED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='逻辑服务';

-- ---------------------------------------------------------------------
-- 2. service_credentials  Executor/业务 API 凭据（与 clients 分表）
-- ---------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS `service_credentials` (
  `id`                 CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'UUID v7，公开凭据 ID（非密钥）',
  `service_id`         CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '所属 services.id',
  `name`               VARCHAR(128) NOT NULL COMMENT '展示名，同服务内唯一',
  `secret_hash`        BINARY(32) NOT NULL COMMENT 'HMAC-SHA256(server_pepper, secret)，用于验签比对',
  `hash_algo`          VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT 'hmac-sha256-v1' COMMENT '密钥哈希算法标识',
  `secret_hint`        VARCHAR(8) CHARACTER SET ascii NULL COMMENT '明文尾缀提示，便于人工辨认',
  `status`             VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'ACTIVE' COMMENT 'ACTIVE=可用；ROTATING=轮换中；REVOKED=已吊销',
  `allowed_job_codes`  JSON NULL COMMENT '可调用的 job.code 数组，支持前缀通配；NULL=本服务全部 Job',
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
  CONSTRAINT `ck_creds_allowed_json` CHECK (`allowed_job_codes` IS NULL OR JSON_TYPE(`allowed_job_codes`) = 'ARRAY')
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

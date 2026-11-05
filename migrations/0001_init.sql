-- =====================================================================
-- Job Center V1.0  初始化迁移 0001_init.sql
-- 目标：MySQL 8.0（已在 8.0.46 上实际执行验证，见 docs/p0-report.md）
-- 约定：
--   * 所有时间列为 DATETIME(3)，存 UTC。应用连接必须设置 time_zone='+00:00'
--     （Go DSN: ?parseTime=true&loc=UTC&time_zone=%27%2B00%3A00%27）。
--     不用 TIMESTAMP（2038 问题且受会话时区隐式转换影响）。
--   * 对外业务 ID（execution_id / attempt_id）为 26 位 ULID，CHAR(26) ASCII。
--   * 状态列用 VARCHAR + CHECK 约束（MySQL 8.0.16+ 强制执行 CHECK），便于新增状态时 ALTER。
--   * 外键策略：配置类低频表使用 FK（ON DELETE RESTRICT）；执行/日志/事件/审计等高频、
--     需归档的表不建 FK，由 Execution Manager 保证引用一致性（见 docs/database.md §4）。
--   * 字符集 utf8mb4 / utf8mb4_0900_ai_ci；标识类列使用 ascii_bin 以节省索引空间并区分大小写。
--   * 本文件只含 CREATE ... IF NOT EXISTS / INSERT IGNORE，可重复执行，不含任何 DROP。
-- =====================================================================

SET NAMES utf8mb4;
SET time_zone = '+00:00';

-- 1. job_service 逻辑服务
CREATE TABLE IF NOT EXISTS `job_service` (
  `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `service_code`  VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '服务编码，^[a-z0-9][a-z0-9._-]{1,63}$',
  `service_name`  VARCHAR(128) NOT NULL,
  `description`   VARCHAR(1000) NULL,
  `status`        VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'ACTIVE' COMMENT 'ACTIVE|DISABLED；DISABLED 拒绝认证与调度',
  `created_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_service_code` (`service_code`),
  CONSTRAINT `ck_service_status` CHECK (`status` IN ('ACTIVE','DISABLED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='逻辑服务';

-- 2. job_service_credential 服务凭据（token = jcs_<key_id>.<secret>；只存 HMAC-SHA256(pepper, secret)）
CREATE TABLE IF NOT EXISTS `job_service_credential` (
  `id`                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `service_id`         BIGINT UNSIGNED NOT NULL,
  `key_id`             VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '公开凭据 ID，用于查找',
  `secret_hash`        BINARY(32) NOT NULL COMMENT 'HMAC-SHA256(server_pepper, secret)',
  `hash_algo`          VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT 'hmac-sha256-v1',
  `secret_hint`        VARCHAR(8) CHARACTER SET ascii NULL COMMENT 'secret 末 4 位，仅展示',
  `status`             VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'ACTIVE' COMMENT 'ACTIVE|ROTATING|REVOKED',
  `allowed_task_codes` JSON NULL COMMENT 'NULL=本服务全部任务；否则 task_code 数组，支持前缀通配 ["erp.*"]',
  `expires_at`         DATETIME(3) NULL,
  `last_used_at`       DATETIME(3) NULL,
  `last_used_ip`       VARCHAR(45) CHARACTER SET ascii NULL,
  `created_by`         VARCHAR(128) NULL,
  `created_at`         DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `revoked_at`         DATETIME(3) NULL,
  `revoked_by`         VARCHAR(128) NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_key_id` (`key_id`),
  KEY `idx_service_status` (`service_id`, `status`),
  CONSTRAINT `fk_cred_service` FOREIGN KEY (`service_id`) REFERENCES `job_service` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_cred_status` CHECK (`status` IN ('ACTIVE','ROTATING','REVOKED')),
  CONSTRAINT `ck_cred_allowed_json` CHECK (`allowed_task_codes` IS NULL OR JSON_TYPE(`allowed_task_codes`) = 'ARRAY')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='服务凭据';

-- 3. job_instance 运行实例（每次进程启动一个新 instance_id；同进程重连沿用）
CREATE TABLE IF NOT EXISTS `job_instance` (
  `id`                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `instance_id`       VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `service_id`        BIGINT UNSIGNED NOT NULL,
  `environment`       VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT 'dev',
  `hostname`          VARCHAR(255) NOT NULL,
  `ip_address`        VARCHAR(45) CHARACTER SET ascii NULL COMMENT '中心观测到的对端 IP',
  `reported_ip`       VARCHAR(45) CHARACTER SET ascii NULL COMMENT '实例自报 IP',
  `pid`               INT UNSIGNED NULL,
  `language`          VARCHAR(16) CHARACTER SET ascii NOT NULL COMMENT 'go|php|java|python...（不建语言专属表）',
  `runtime_version`   VARCHAR(64) NULL,
  `sdk_version`       VARCHAR(32) NULL,
  `protocol_version`  VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT '1.0',
  `app_version`       VARCHAR(64) NULL,
  `git_commit`        VARCHAR(64) CHARACTER SET ascii NULL,
  `build_time`        DATETIME(3) NULL,
  `started_at`        DATETIME(3) NOT NULL COMMENT '进程启动时间（自报）',
  `registered_at`     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `connected_at`      DATETIME(3) NULL,
  `disconnected_at`   DATETIME(3) NULL,
  `last_heartbeat_at` DATETIME(3) NULL,
  `status`            VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'REGISTERING' COMMENT 'REGISTERING|ONLINE|SUSPECT|OFFLINE|DRAINING',
  `max_concurrency`   INT UNSIGNED NOT NULL DEFAULT 1,
  `running_count`     INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '心跳上报值，展示用；容量判定以 attempt 表为准',
  `session_epoch`     BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '每次成功认证 +1，旧连接据此失效',
  `gateway_node_id`   VARCHAR(64) CHARACTER SET ascii NULL,
  `connection_id`     VARCHAR(64) CHARACTER SET ascii NULL COMMENT '仅诊断，不作身份',
  `metadata`          JSON NULL,
  `created_at`        DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`        DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_instance_id` (`instance_id`),
  KEY `idx_service_status` (`service_id`, `status`),
  KEY `idx_status_heartbeat` (`status`, `last_heartbeat_at`),
  KEY `idx_service_commit` (`service_id`, `git_commit`),
  CONSTRAINT `fk_instance_service` FOREIGN KEY (`service_id`) REFERENCES `job_service` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_instance_status` CHECK (`status` IN ('REGISTERING','ONLINE','SUSPECT','OFFLINE','DRAINING')),
  CONSTRAINT `ck_instance_meta_json` CHECK (`metadata` IS NULL OR JSON_TYPE(`metadata`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='运行实例';

-- 4. job_definition 任务能力定义（逻辑任务，服务内唯一）
CREATE TABLE IF NOT EXISTS `job_definition` (
  `id`                      BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `service_id`              BIGINT UNSIGNED NOT NULL,
  `task_code`               VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `task_name`               VARCHAR(128) NOT NULL,
  `description`             VARCHAR(1000) NULL,
  `handler_version`         VARCHAR(32) CHARACTER SET ascii NOT NULL COMMENT '最近一次注册的版本（展示）',
  `input_schema`            JSON NULL COMMENT '最近版本的参数 JSON Schema',
  `schema_hash`             CHAR(64) CHARACTER SET ascii NULL COMMENT '规范化 schema 的 SHA-256',
  `default_cron`            VARCHAR(128) CHARACTER SET ascii NULL COMMENT 'Executor 建议的 6 字段 Cron，仅初始化 job_schedule 时使用',
  `default_timeout_seconds` INT UNSIGNED NULL,
  `status`                  VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'ACTIVE' COMMENT 'ACTIVE|ORPHANED|DISABLED',
  `first_registered_at`     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `last_registered_at`      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `created_at`              DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`              DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_service_task` (`service_id`, `task_code`),
  KEY `idx_task_code` (`task_code`),
  CONSTRAINT `fk_def_service` FOREIGN KEY (`service_id`) REFERENCES `job_service` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_def_status` CHECK (`status` IN ('ACTIVE','ORPHANED','DISABLED')),
  CONSTRAINT `ck_def_schema_json` CHECK (`input_schema` IS NULL OR JSON_TYPE(`input_schema`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='任务能力定义';

-- 4b. job_definition_version 任务定义版本历史（按 handler 主版本取最新 schema；主版本相同即兼容）
CREATE TABLE IF NOT EXISTS `job_definition_version` (
  `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `definition_id`   BIGINT UNSIGNED NOT NULL,
  `handler_version` VARCHAR(32) CHARACTER SET ascii NOT NULL,
  `handler_major`   INT UNSIGNED NOT NULL COMMENT 'handler_version 主版本号；主版本相同即兼容',
  `schema_hash`     CHAR(64) CHARACTER SET ascii NOT NULL COMMENT '无 schema 时取 {} 的 hash',
  `input_schema`    JSON NULL,
  `first_seen_at`   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `last_seen_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_def_ver` (`definition_id`, `handler_version`, `schema_hash`),
  KEY `idx_def_major` (`definition_id`, `handler_major`, `id`),
  CONSTRAINT `fk_defver_def` FOREIGN KEY (`definition_id`) REFERENCES `job_definition` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='任务定义版本历史';

-- 5. job_instance_task 实例提供的任务能力（多对多，任务不永久绑定实例）
CREATE TABLE IF NOT EXISTS `job_instance_task` (
  `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `instance_pk`     BIGINT UNSIGNED NOT NULL COMMENT 'job_instance.id',
  `definition_id`   BIGINT UNSIGNED NOT NULL,
  `handler_version` VARCHAR(32) CHARACTER SET ascii NOT NULL,
  `handler_major`   INT UNSIGNED NOT NULL COMMENT '派发兼容判断：= job_execution.handler_major',
  `schema_hash`     CHAR(64) CHARACTER SET ascii NOT NULL,
  `max_concurrency` INT UNSIGNED NULL COMMENT '该实例上此任务并发上限，NULL=不单独限制',
  `registered_at`   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_instance_def` (`instance_pk`, `definition_id`),
  KEY `idx_def_major` (`definition_id`, `handler_major`),
  CONSTRAINT `fk_it_instance` FOREIGN KEY (`instance_pk`) REFERENCES `job_instance` (`id`) ON DELETE CASCADE ON UPDATE RESTRICT,
  CONSTRAINT `fk_it_def` FOREIGN KEY (`definition_id`) REFERENCES `job_definition` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='实例任务能力';

-- 6. job_schedule 调度配置（Job Center 管理；重新注册不覆盖 config_source=ADMIN 的记录）
CREATE TABLE IF NOT EXISTS `job_schedule` (
  `id`                         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `task_id`                    BIGINT UNSIGNED NOT NULL COMMENT 'job_definition.id；V1 每任务一条',
  `schedule_type`              VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'NONE' COMMENT 'CRON|NONE；预留 DELAY|ONCE',
  `cron_expression`            VARCHAR(128) CHARACTER SET ascii NULL COMMENT '严格 6 字段：秒 分 时 日 月 周',
  `timezone`                   VARCHAR(64) CHARACTER SET ascii NULL DEFAULT NULL COMMENT 'IANA 时区名；NULL=Job Center 服务器本地时区（TZ 或 /etc/localtime）',
  `enabled`                    TINYINT(1) NOT NULL DEFAULT 0 COMMENT 'Cron 自动调度开关（新注册默认关闭）',
  `manual_enabled`             TINYINT(1) NOT NULL DEFAULT 1 COMMENT '手动/API 触发开关，与 enabled 独立',
  `timeout_seconds`            INT UNSIGNED NOT NULL DEFAULT 3600,
  `concurrency_policy`         VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'SKIP' COMMENT 'ALLOW|SKIP|QUEUE',
  `concurrency_scope`          VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'TASK' COMMENT 'TASK|SERVICE|IDEMPOTENCY_KEY',
  `max_concurrency`            INT UNSIGNED NOT NULL DEFAULT 1 COMMENT 'ALLOW 时上限',
  `queue_max`                  INT UNSIGNED NOT NULL DEFAULT 10 COMMENT 'QUEUE 时最大排队数，超出 SKIPPED',
  `retry_policy`               JSON NOT NULL COMMENT '见 docs/execution-lifecycle.md §6',
  `misfire_policy`             VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'SKIP' COMMENT 'SKIP|FIRE_ONCE|CATCH_UP',
  `misfire_threshold_seconds`  INT UNSIGNED NOT NULL DEFAULT 60,
  `catch_up_max`               INT UNSIGNED NOT NULL DEFAULT 10 COMMENT 'CATCH_UP 单次最多补跑条数（≤100）',
  `dispatch_strategy`          VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'LEAST_LOAD' COMMENT 'LEAST_LOAD|ROUND_ROBIN|RANDOM',
  `next_fire_at`               DATETIME(3) NULL,
  `last_fire_at`               DATETIME(3) NULL,
  `config_source`              VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'DEFAULT' COMMENT 'DEFAULT|ADMIN',
  `version`                    INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '乐观锁',
  `updated_by`                 VARCHAR(128) NULL,
  `created_at`                 DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`                 DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_task` (`task_id`),
  KEY `idx_due` (`enabled`, `next_fire_at`),
  CONSTRAINT `fk_schedule_task` FOREIGN KEY (`task_id`) REFERENCES `job_definition` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_sch_type` CHECK (`schedule_type` IN ('CRON','NONE')),
  CONSTRAINT `ck_sch_cron` CHECK (`schedule_type` <> 'CRON' OR `cron_expression` IS NOT NULL),
  CONSTRAINT `ck_sch_conc` CHECK (`concurrency_policy` IN ('ALLOW','SKIP','QUEUE')),
  CONSTRAINT `ck_sch_scope` CHECK (`concurrency_scope` IN ('TASK','SERVICE','IDEMPOTENCY_KEY')),
  CONSTRAINT `ck_sch_misfire` CHECK (`misfire_policy` IN ('SKIP','FIRE_ONCE','CATCH_UP')),
  CONSTRAINT `ck_sch_catchup` CHECK (`catch_up_max` BETWEEN 1 AND 100),
  CONSTRAINT `ck_sch_timeout` CHECK (`timeout_seconds` BETWEEN 1 AND 604800),
  CONSTRAINT `ck_sch_retry_json` CHECK (JSON_TYPE(`retry_policy`) = 'OBJECT'),
  CONSTRAINT `ck_sch_source` CHECK (`config_source` IN ('DEFAULT','ADMIN'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='调度配置';

-- 7. job_execution 逻辑执行
CREATE TABLE IF NOT EXISTS `job_execution` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `execution_id`        CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'ULID',
  `task_id`             BIGINT UNSIGNED NOT NULL COMMENT 'job_definition.id（无 FK）',
  `service_id`          BIGINT UNSIGNED NOT NULL COMMENT '冗余',
  `task_code`           VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '冗余快照',
  `schedule_id`         BIGINT UNSIGNED NULL,
  `trigger_type`        VARCHAR(16) CHARACTER SET ascii NOT NULL COMMENT 'CRON|MANUAL|API|CATCH_UP',
  `triggered_by`        VARCHAR(128) NOT NULL COMMENT 'system:scheduler | admin:<id> | api:<client>',
  `trigger_context`     JSON NULL COMMENT '{operator_name, client_ip, request_id, trace_id, reason}',
  `params`              JSON NULL,
  `idempotency_key`     VARCHAR(128) NULL,
  `status`              VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'PENDING',
  `status_reason`       VARCHAR(64) CHARACTER SET ascii NULL COMMENT 'CONCURRENCY_SKIP|NO_INSTANCE|MISFIRE_SKIP|RETRY_EXHAUSTED|...',
  `handler_version`     VARCHAR(32) CHARACTER SET ascii NULL COMMENT '创建时该主版本最新已注册版本（快照）',
  `handler_major`       INT UNSIGNED NULL COMMENT '只派给 job_instance_task.handler_major 相同的实例',
  `schema_hash`         CHAR(64) CHARACTER SET ascii NULL COMMENT '参数据以校验的 schema（审计用）',
  `timeout_seconds`     INT UNSIGNED NOT NULL COMMENT '快照',
  `retry_policy`        JSON NOT NULL COMMENT '快照',
  `max_attempts`        INT UNSIGNED NOT NULL DEFAULT 1,
  `attempt_count`       INT UNSIGNED NOT NULL DEFAULT 0,
  `current_attempt_id`  CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  `concurrency_slot`    VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT 'SKIP/QUEUE 非终态占位（如 task:12:run）；终态置 NULL',
  `next_attempt_at`     DATETIME(3) NULL COMMENT '待派发/重试退避到期时间',
  `deadline_at`         DATETIME(3) NULL,
  `cancel_requested_at` DATETIME(3) NULL,
  `cancel_requested_by` VARCHAR(128) NULL,
  `result`              JSON NULL COMMENT '≤64KB',
  `error_code`          VARCHAR(64) CHARACTER SET ascii NULL,
  `error_message`       VARCHAR(2000) NULL,
  `scheduled_at`        DATETIME(3) NULL COMMENT 'Cron 计划时间；手动为请求时间',
  `queued_at`           DATETIME(3) NULL,
  `started_at`          DATETIME(3) NULL,
  `finished_at`         DATETIME(3) NULL,
  `version`             INT UNSIGNED NOT NULL DEFAULT 0,
  `created_at`          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_execution_id` (`execution_id`),
  UNIQUE KEY `uk_schedule_tick` (`schedule_id`, `scheduled_at`),
  UNIQUE KEY `uk_task_idem` (`task_id`, `idempotency_key`),
  UNIQUE KEY `uk_concurrency_slot` (`concurrency_slot`),
  KEY `idx_dispatch` (`status`, `next_attempt_at`),
  KEY `idx_task_created` (`task_id`, `created_at`),
  KEY `idx_task_status` (`task_id`, `status`),
  KEY `idx_service_status` (`service_id`, `status`),
  KEY `idx_created` (`created_at`),
  CONSTRAINT `ck_exec_status` CHECK (`status` IN ('PENDING','QUEUED','RUNNING','SUCCEEDED','FAILED','CANCELLED','TIMED_OUT','SKIPPED')),
  CONSTRAINT `ck_exec_trigger` CHECK (`trigger_type` IN ('CRON','MANUAL','API','CATCH_UP')),
  CONSTRAINT `ck_exec_params_json` CHECK (`params` IS NULL OR JSON_TYPE(`params`) = 'OBJECT'),
  CONSTRAINT `ck_exec_retry_json` CHECK (JSON_TYPE(`retry_policy`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='逻辑执行';

-- 8. job_execution_attempt 执行尝试（含租约与 fencing token）
CREATE TABLE IF NOT EXISTS `job_execution_attempt` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `attempt_id`          CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'ULID',
  `execution_id`        CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `attempt_number`      INT UNSIGNED NOT NULL COMMENT '从 1 开始',
  `task_id`             BIGINT UNSIGNED NOT NULL,
  `service_id`          BIGINT UNSIGNED NOT NULL,
  `instance_id`         VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  `hostname`            VARCHAR(255) NULL COMMENT '快照',
  `git_commit`          VARCHAR(64) CHARACTER SET ascii NULL COMMENT '分配时实例版本快照',
  `app_version`         VARCHAR(64) NULL,
  `handler_version`     VARCHAR(32) CHARACTER SET ascii NULL,
  `lease_token`         CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '128bit 随机 hex，每次分配新生成',
  `lease_expires_at`    DATETIME(3) NULL,
  `last_heartbeat_at`   DATETIME(3) NULL,
  `status`              VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'CREATED',
  `status_reason`       VARCHAR(64) CHARACTER SET ascii NULL,
  `dispatch_count`      INT UNSIGNED NOT NULL DEFAULT 0,
  `dispatched_at`       DATETIME(3) NULL,
  `acked_at`            DATETIME(3) NULL,
  `started_at`          DATETIME(3) NULL,
  `finished_at`         DATETIME(3) NULL,
  `deadline_at`         DATETIME(3) NULL,
  `duration_ms`         BIGINT UNSIGNED NULL,
  `progress_percent`    DECIMAL(5,2) NULL,
  `progress_message`    VARCHAR(500) NULL,
  `progress_updated_at` DATETIME(3) NULL,
  `cancel_state`        VARCHAR(24) CHARACTER SET ascii NOT NULL DEFAULT 'NONE' COMMENT 'NONE|REQUESTED|SENT|ACKED|STOPPED|FAILED|CANCEL_UNCONFIRMED（租约过期仍未收到 task.cancelled）',
  `cancel_requested_at` DATETIME(3) NULL,
  `cancel_acked_at`     DATETIME(3) NULL,
  `exit_code`           INT NULL,
  `error_code`          VARCHAR(64) CHARACTER SET ascii NULL,
  `error_message`       VARCHAR(2000) NULL,
  `error_detail`        TEXT NULL COMMENT '堆栈等，≤16KB',
  `result`              JSON NULL COMMENT '≤64KB',
  `last_log_seq`        BIGINT UNSIGNED NOT NULL DEFAULT 0,
  `lost_at`             DATETIME(3) NULL,
  `version`             INT UNSIGNED NOT NULL DEFAULT 0,
  `created_at`          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_attempt_id` (`attempt_id`),
  UNIQUE KEY `uk_exec_number` (`execution_id`, `attempt_number`),
  KEY `idx_status_lease` (`status`, `lease_expires_at`),
  KEY `idx_status_deadline` (`status`, `deadline_at`),
  KEY `idx_instance_status` (`instance_id`, `status`),
  KEY `idx_task_created` (`task_id`, `created_at`),
  KEY `idx_created` (`created_at`),
  CONSTRAINT `ck_att_status` CHECK (`status` IN ('CREATED','DISPATCHING','ACKNOWLEDGED','RUNNING','SUCCEEDED','FAILED','CANCELLED','TIMED_OUT','LOST')),
  CONSTRAINT `ck_att_cancel` CHECK (`cancel_state` IN ('NONE','REQUESTED','SENT','ACKED','STOPPED','FAILED','CANCEL_UNCONFIRMED')),
  CONSTRAINT `ck_att_progress` CHECK (`progress_percent` IS NULL OR (`progress_percent` >= 0 AND `progress_percent` <= 100))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='执行尝试';

-- 9. job_execution_log 执行日志（按月 RANGE COLUMNS 分区；在线保留 365 天，归档成功后 DROP PARTITION）
--    分区表主键须含分区列，故 (attempt_id,sequence) 不再唯一；去重靠 job_execution_attempt.last_log_seq。
--    archiver 始终维护未来 3 个月分区（REORGANIZE pmax）。
CREATE TABLE IF NOT EXISTS `job_execution_log` (
  `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `execution_id`  CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `attempt_id`    CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `sequence`      BIGINT UNSIGNED NOT NULL COMMENT 'Executor 端按 attempt 单调递增，从 1 开始',
  `level`         VARCHAR(8) CHARACTER SET ascii NOT NULL COMMENT 'DEBUG|INFO|WARN|ERROR',
  `stream`        VARCHAR(8) CHARACTER SET ascii NULL COMMENT 'stdout|stderr（PHP 子进程），SDK 结构化日志为 NULL',
  `message`       TEXT NOT NULL COMMENT '已脱敏，单条 ≤8KB，超出截断',
  `fields`        JSON NULL,
  `truncated`     TINYINT(1) NOT NULL DEFAULT 0,
  `logged_at`     DATETIME(3) NOT NULL COMMENT 'Executor 时间',
  `created_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '入库时间',
  PRIMARY KEY (`id`, `created_at`),
  KEY `idx_attempt_seq` (`attempt_id`, `sequence`),
  KEY `idx_exec_attempt_seq` (`execution_id`, `attempt_id`, `sequence`),
  KEY `idx_created` (`created_at`),
  CONSTRAINT `ck_log_level` CHECK (`level` IN ('DEBUG','INFO','WARN','ERROR')),
  CONSTRAINT `ck_log_fields_json` CHECK (`fields` IS NULL OR JSON_TYPE(`fields`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='执行日志（按月分区）'
PARTITION BY RANGE COLUMNS(`created_at`) (
  PARTITION p202610 VALUES LESS THAN ('2026-11-01 00:00:00'),
  PARTITION p202611 VALUES LESS THAN ('2026-12-01 00:00:00'),
  PARTITION p202612 VALUES LESS THAN ('2027-01-01 00:00:00'),
  PARTITION p202701 VALUES LESS THAN ('2027-02-01 00:00:00'),
  PARTITION p202702 VALUES LESS THAN ('2027-03-01 00:00:00'),
  PARTITION pmax    VALUES LESS THAN (MAXVALUE)
);

-- 10. job_execution_event 执行事件（仅追加；状态迁移审计 + SSE 状态事件源）
CREATE TABLE IF NOT EXISTS `job_execution_event` (
  `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `execution_id`  CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `attempt_id`    CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  `event_type`    VARCHAR(32) CHARACTER SET ascii NOT NULL COMMENT 'EXECUTION_STATE|ATTEMPT_STATE|DISPATCHED|DISPATCH_RETRY|ACKED|PROGRESS|LEASE_EXPIRED|CANCEL_REQUESTED|CANCEL_ACKED|STALE_RESULT_REJECTED|RETRY_SCHEDULED|NOTE',
  `from_status`   VARCHAR(16) CHARACTER SET ascii NULL,
  `to_status`     VARCHAR(16) CHARACTER SET ascii NULL,
  `actor_type`    VARCHAR(16) CHARACTER SET ascii NOT NULL COMMENT 'SYSTEM|ADMIN|EXECUTOR|API',
  `actor_id`      VARCHAR(128) NULL,
  `instance_id`   VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  `payload`       JSON NULL,
  `trace_id`      VARCHAR(64) CHARACTER SET ascii NULL,
  `request_id`    VARCHAR(64) CHARACTER SET ascii NULL,
  `created_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_exec_id` (`execution_id`, `id`),
  KEY `idx_type_created` (`event_type`, `created_at`),
  KEY `idx_created` (`created_at`),
  CONSTRAINT `ck_evt_actor` CHECK (`actor_type` IN ('SYSTEM','ADMIN','EXECUTOR','API'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='执行事件';

-- 11. job_operation_audit 管理操作审计
CREATE TABLE IF NOT EXISTS `job_operation_audit` (
  `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `operator_source` VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT 'glassadmin' COMMENT '签名客户端 ID',
  `operator_id`     VARCHAR(64) NOT NULL,
  `operator_name`   VARCHAR(128) NULL,
  `action`          VARCHAR(64) CHARACTER SET ascii NOT NULL COMMENT 'task.execute|schedule.update|credential.create|...',
  `permission`      VARCHAR(64) CHARACTER SET ascii NULL COMMENT '如 job.task.execute',
  `resource_type`   VARCHAR(32) CHARACTER SET ascii NOT NULL,
  `resource_id`     VARCHAR(128) NULL,
  `request_id`      VARCHAR(64) CHARACTER SET ascii NULL,
  `trace_id`        VARCHAR(64) CHARACTER SET ascii NULL,
  `client_ip`       VARCHAR(45) CHARACTER SET ascii NULL COMMENT '浏览器 IP（GlassAdmin 签名转发）',
  `before_data`     JSON NULL,
  `after_data`      JSON NULL,
  `result`          VARCHAR(16) CHARACTER SET ascii NOT NULL COMMENT 'SUCCESS|DENIED|FAILED',
  `error_message`   VARCHAR(1000) NULL,
  `created_at`      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_operator_created` (`operator_source`, `operator_id`, `created_at`),
  KEY `idx_resource` (`resource_type`, `resource_id`),
  KEY `idx_created` (`created_at`),
  CONSTRAINT `ck_audit_result` CHECK (`result` IN ('SUCCESS','DENIED','FAILED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='管理操作审计';

-- 12. job_deployment 预期发布版本及部署记录
CREATE TABLE IF NOT EXISTS `job_deployment` (
  `id`                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `service_id`         BIGINT UNSIGNED NOT NULL,
  `environment`        VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT 'dev',
  `deployment_version` VARCHAR(64) NOT NULL,
  `git_commit`         VARCHAR(64) CHARACTER SET ascii NOT NULL,
  `build_id`           VARCHAR(128) CHARACTER SET ascii NOT NULL COMMENT '发布方构建号（发布脚本生成；手工登记为 manual-<时间>）；与 service+env 组成幂等键',
  `build_url`          VARCHAR(500) NULL,
  `deployment_status`  VARCHAR(16) CHARACTER SET ascii NOT NULL COMMENT 'PENDING|DEPLOYING|SUCCEEDED|FAILED|ROLLED_BACK',
  `expected_key`       VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL COMMENT '为当前预期版本时 = <service_id>:<environment>，否则 NULL',
  `source`             VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'API' COMMENT 'API=签名发布登记接口 POST /v1/deployments|MANUAL=管理端手工登记',
  `triggered_by`       VARCHAR(128) NULL,
  `payload`            JSON NULL COMMENT '登记请求原文（脱敏）',
  `deployed_at`        DATETIME(3) NULL,
  `created_at`         DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at`         DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_build` (`service_id`, `environment`, `build_id`),
  UNIQUE KEY `uk_expected` (`expected_key`),
  KEY `idx_service_env_created` (`service_id`, `environment`, `created_at`),
  CONSTRAINT `fk_deploy_service` FOREIGN KEY (`service_id`) REFERENCES `job_service` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_deploy_status` CHECK (`deployment_status` IN ('PENDING','DEPLOYING','SUCCEEDED','FAILED','ROLLED_BACK')),
  CONSTRAINT `ck_deploy_source` CHECK (`source` IN ('API','MANUAL'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='发布记录与预期版本';

-- 13. job_node_lease 调度节点租约（V1 单节点也启用，为多节点 leader 预留）
CREATE TABLE IF NOT EXISTS `job_node_lease` (
  `lease_name`     VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'scheduler|lease-sweeper|archiver',
  `owner_node_id`  VARCHAR(64) CHARACTER SET ascii NOT NULL,
  `fencing_token`  BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '每次换主 +1',
  `lease_until`    DATETIME(3) NOT NULL,
  `updated_at`     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`lease_name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='调度节点租约';

-- 13b. job_archive_run 每日归档执行记录（导出成功 EXPORTED 后才允许删除；每表每天一个文件）
CREATE TABLE IF NOT EXISTS `job_archive_run` (
  `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `table_name`    VARCHAR(64) CHARACTER SET ascii NOT NULL,
  `archive_date`  DATE NOT NULL COMMENT 'UTC 自然日',
  `status`        VARCHAR(16) CHARACTER SET ascii NOT NULL DEFAULT 'RUNNING' COMMENT 'RUNNING|EXPORTED|SUCCEEDED|FAILED',
  `file_uri`      VARCHAR(500) NULL COMMENT '本地路径或对象存储 URI（*.jsonl.gz）',
  `row_count`     BIGINT UNSIGNED NULL,
  `file_bytes`    BIGINT UNSIGNED NULL,
  `checksum`      CHAR(64) CHARACTER SET ascii NULL COMMENT '文件 SHA-256',
  `deleted_rows`  BIGINT UNSIGNED NULL,
  `error_message` VARCHAR(2000) NULL,
  `attempts`      INT UNSIGNED NOT NULL DEFAULT 1,
  `started_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `exported_at`   DATETIME(3) NULL,
  `finished_at`   DATETIME(3) NULL,
  `updated_at`    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_table_date` (`table_name`, `archive_date`),
  KEY `idx_status_date` (`status`, `archive_date`),
  CONSTRAINT `ck_arch_status` CHECK (`status` IN ('RUNNING','EXPORTED','SUCCEEDED','FAILED')),
  CONSTRAINT `ck_arch_delete_after_export` CHECK (`status` <> 'SUCCEEDED' OR (`file_uri` IS NOT NULL AND `checksum` IS NOT NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='每日归档执行记录';

-- 14. job_schema_migration 迁移版本记录
CREATE TABLE IF NOT EXISTS `job_schema_migration` (
  `version`     VARCHAR(32) CHARACTER SET ascii NOT NULL,
  `description` VARCHAR(255) NOT NULL,
  `checksum`    CHAR(64) CHARACTER SET ascii NULL,
  `applied_at`  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`version`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='迁移记录';

INSERT IGNORE INTO `job_schema_migration` (`version`, `description`) VALUES ('0001', 'init');

-- =============================================================================
-- Job Center V1.0  增量迁移 0002_task_state_and_default_params.sql
-- 任务原生持久化游标（job_task_state）+ 定时默认参数（job_schedule.default_params）
-- =============================================================================

SET NAMES utf8mb4;

SET @jc_has_default_params := (
  SELECT COUNT(*)
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'job_schedule'
    AND COLUMN_NAME = 'default_params'
);
SET @jc_alter_sched := IF(
  @jc_has_default_params = 0,
  'ALTER TABLE `job_schedule` ADD COLUMN `default_params` JSON NULL COMMENT ''Cron/Catch-up 触发默认参数（OBJECT，NULL 触发时取 {}）'' AFTER `timezone`, ADD CONSTRAINT `ck_sched_default_params` CHECK (`default_params` IS NULL OR JSON_TYPE(`default_params`) = ''OBJECT'')',
  'DO 0'
);
PREPARE jc_stmt_0002 FROM @jc_alter_sched;
EXECUTE jc_stmt_0002;
DEALLOCATE PREPARE jc_stmt_0002;

CREATE TABLE IF NOT EXISTS `job_task_state` (
  `task_id`           BIGINT UNSIGNED NOT NULL,
  `state`             JSON NOT NULL COMMENT '任务持久化游标/状态对象（<= 64 KiB）',
  `version`           BIGINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '单调递增乐观锁版本；未建行时逻辑视图为 state={}, version=0',
  `updated_by`        VARCHAR(128) NOT NULL COMMENT 'attempt:<attempt_id> 或 admin:<operator_id>',
  `last_execution_id` CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  `last_attempt_id`   CHAR(26) CHARACTER SET ascii COLLATE ascii_bin NULL,
  `updated_at`        DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`task_id`),
  CONSTRAINT `fk_task_state_def` FOREIGN KEY (`task_id`) REFERENCES `job_definition` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT `ck_task_state_json` CHECK (JSON_TYPE(`state`) = 'OBJECT')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='任务原生持久化游标/状态';

INSERT IGNORE INTO `job_schema_migration` (`version`, `description`) VALUES ('0002', 'task_state_and_default_params');

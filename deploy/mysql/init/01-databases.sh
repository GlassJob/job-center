#!/bin/bash
# MySQL 官方镜像首次初始化时执行：建两个库、每库一个账号且只授权本库。
# job-admin 不得访问 job_center（只走 Job Center HTTP API）。
# 已确认（2026-11-05）：不建第三库；job-admin 自有表用 ga_job_ 前缀放进 job_admin。
set -euo pipefail
mysql -uroot -p"${MYSQL_ROOT_PASSWORD}" <<SQL
CREATE DATABASE IF NOT EXISTS job_center CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE IF NOT EXISTS job_admin  CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE USER IF NOT EXISTS 'job_center'@'%' IDENTIFIED BY '${GJ_CENTER_PASSWORD}';
CREATE USER IF NOT EXISTS 'job_admin'@'%'  IDENTIFIED BY '${GJ_ADMIN_PASSWORD}';
GRANT ALL PRIVILEGES ON job_center.* TO 'job_center'@'%';
GRANT ALL PRIVILEGES ON job_admin.*  TO 'job_admin'@'%';
FLUSH PRIVILEGES;
SQL

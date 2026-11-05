#!/bin/bash
# MySQL 官方镜像首次初始化时执行：建三个库、每库一个账号且只授权本库。
# job-admin 不得访问 glassjob_center（只走 Job Center HTTP API）。
set -euo pipefail
mysql -uroot -p"${MYSQL_ROOT_PASSWORD}" <<SQL
CREATE DATABASE IF NOT EXISTS glassjob_center    CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE IF NOT EXISTS glassjob_admin     CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE IF NOT EXISTS glassjob_admin_biz CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE USER IF NOT EXISTS 'glassjob_center'@'%'    IDENTIFIED BY '${GJ_CENTER_PASSWORD}';
CREATE USER IF NOT EXISTS 'glassjob_admin'@'%'     IDENTIFIED BY '${GJ_ADMIN_PASSWORD}';
CREATE USER IF NOT EXISTS 'glassjob_admin_biz'@'%' IDENTIFIED BY '${GJ_ADMIN_BIZ_PASSWORD}';
GRANT ALL PRIVILEGES ON glassjob_center.*    TO 'glassjob_center'@'%';
GRANT ALL PRIVILEGES ON glassjob_admin.*     TO 'glassjob_admin'@'%';
GRANT ALL PRIVILEGES ON glassjob_admin_biz.* TO 'glassjob_admin_biz'@'%';
FLUSH PRIVILEGES;
SQL

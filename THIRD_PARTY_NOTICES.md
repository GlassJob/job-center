# Third-party licensing / 第三方许可

核查日期：2026-11-18；代码基线：`0a180db`。本仓库自有代码采用 Apache-2.0，以下第三方代码**不因此被重新许可**。

## 核查范围与证据

- 核查基线全部 157 个 Git 跟踪文件的文件清单与许可/版权/来源标记，以及提交作者记录；未发现随仓库提交的 vendor、字体、图片或第三方压缩前端产物。未发现现存项目 LICENSE；新增许可不覆盖依赖原有条款。
- 核查 `go.mod` 明确列出的 48 个固定版本（10 个直接、38 个间接依赖），许可原文从 `go mod download -json` 返回的对应版本模块源目录提取。包含本机默认构建未启用的可选依赖，不宣称每个模块都链接进当前二进制。
- 原样保留模块内的 LICENSE、NOTICE、AUTHORS、PATENTS 及相关嵌套许可文件，见下表；[机器可读清单](third_party/manifest.json) 记录模块校验和、各保留文件 SHA-256。Go 标准库原许可另见 [Go LICENSE](third_party/licenses/go/LICENSE) 和 [PATENTS](third_party/licenses/go/PATENTS)。
- 使用 `github.com/google/go-licenses@v1.6.0 report ./...` 对默认 linux/amd64 构建交叉检查，结果见 [扫描记录](third_party/go-licenses-linux-amd64.csv)。该工具将 YAML 复合许可证简化成 MIT；本清单按实际原文同时保留 MIT 与 Apache-2.0 条款。

## 分发要求与例外

- `github.com/go-sql-driver/mysql@v1.8.1` 是 **MPL-2.0**，不是 Apache-2.0。保留其原许可；分发包含驱动的可执行文件时，应告知接收者如何取得其 MPL 覆盖文件的源代码，并满足 MPL 第 3 节。此版本源代码可从 [固定版本源码归档](https://proxy.golang.org/github.com/go-sql-driver/mysql/@v/v1.8.1.zip) 取得；若修改驱动覆盖文件，须同时提供相应修改源码，而不能只链接原版。MPL 的文件级要求不意味着本项目所有独立自有文件都改用 MPL。
- MIT、BSD、ISC 的版权/许可/免责声明随分发物保留；Apache 组件的必要归属与 NOTICE 不删除。两个 YAML 模块均包含文件级 MIT / Apache 条款，原 LICENSE 与 NOTICE 均已保存。
- sonic 还包含 `licenses/` 下的 Go、simdjson、golang-asm、yyjson、eisel_lemire、Drachennest 归属，全部原样保留，不能只携带其根 Apache 文本。
- 发布二进制、容器或源码包时随附本清单和 `third_party/licenses/`；若构建工具链、构建标签、平台或依赖版本变化，应重新核查。没有新增项目级 NOTICE；上游 NOTICE 在各组件目录中保留并由本清单引用。

## 边界

本核查未发现上述固定模块缺少许可原文或需要覆盖原许可的冲突；不是法律意见或完整代码相似性/权属审计。扫描器对汇编等非 Go 文件不分析其进一步依赖；本清单已保留所在模块及可见嵌套许可，但不宣称验证了所有构建变体。提交作者记录不能证明雇佣关系、CLA 或全部贡献授权，项目所有者仍须确认自有代码的授权权利。未改仓库可见性、历史提交或运行中的服务；未给第三方文件新增 Apache 头。

## 固定版本模块

| Module | Version | License | Preserved evidence |
|---|---|---|---|
| `github.com/coder/websocket` | `v1.8.12` | ISC | [original files](third_party/licenses/github.com/coder/websocket/v1.8.12/) |
| `github.com/gin-gonic/gin` | `v1.10.0` | MIT | [original files](third_party/licenses/github.com/gin-gonic/gin/v1.10.0/) |
| `github.com/go-sql-driver/mysql` | `v1.8.1` | MPL-2.0 | [original files](third_party/licenses/github.com/go-sql-driver/mysql/v1.8.1/) |
| `github.com/golang-migrate/migrate/v4` | `v4.20.1` | MIT | [original files](third_party/licenses/github.com/golang-migrate/migrate/v4/v4.20.1/) |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause | [original files](third_party/licenses/github.com/google/uuid/v1.6.0/) |
| `github.com/robfig/cron/v3` | `v3.0.1` | MIT | [original files](third_party/licenses/github.com/robfig/cron/v3/v3.0.1/) |
| `github.com/santhosh-tekuri/jsonschema/v6` | `v6.0.1` | Apache-2.0 | [original files](third_party/licenses/github.com/santhosh-tekuri/jsonschema/v6/v6.0.1/) |
| `github.com/spf13/viper` | `v1.21.0` | MIT | [original files](third_party/licenses/github.com/spf13/viper/v1.21.0/) |
| `gorm.io/driver/mysql` | `v1.5.7` | MIT | [original files](third_party/licenses/gorm.io/driver/mysql/v1.5.7/) |
| `gorm.io/gorm` | `v1.25.12` | MIT | [original files](third_party/licenses/gorm.io/gorm/v1.25.12/) |
| `filippo.io/edwards25519` | `v1.1.0` | BSD-3-Clause | [original files](third_party/licenses/filippo.io/edwards25519/v1.1.0/) |
| `github.com/bytedance/sonic` | `v1.11.6` | Apache-2.0 | [original files](third_party/licenses/github.com/bytedance/sonic/v1.11.6/) |
| `github.com/bytedance/sonic/loader` | `v0.1.1` | Apache-2.0 | [original files](third_party/licenses/github.com/bytedance/sonic/loader/v0.1.1/) |
| `github.com/cloudwego/base64x` | `v0.1.4` | Apache-2.0 | [original files](third_party/licenses/github.com/cloudwego/base64x/v0.1.4/) |
| `github.com/cloudwego/iasm` | `v0.2.0` | Apache-2.0 | [original files](third_party/licenses/github.com/cloudwego/iasm/v0.2.0/) |
| `github.com/fsnotify/fsnotify` | `v1.9.0` | BSD-3-Clause | [original files](third_party/licenses/github.com/fsnotify/fsnotify/v1.9.0/) |
| `github.com/gabriel-vasile/mimetype` | `v1.4.3` | MIT | [original files](third_party/licenses/github.com/gabriel-vasile/mimetype/v1.4.3/) |
| `github.com/gin-contrib/sse` | `v0.1.0` | MIT | [original files](third_party/licenses/github.com/gin-contrib/sse/v0.1.0/) |
| `github.com/go-playground/locales` | `v0.14.1` | MIT | [original files](third_party/licenses/github.com/go-playground/locales/v0.14.1/) |
| `github.com/go-playground/universal-translator` | `v0.18.1` | MIT | [original files](third_party/licenses/github.com/go-playground/universal-translator/v0.18.1/) |
| `github.com/go-playground/validator/v10` | `v10.20.0` | MIT | [original files](third_party/licenses/github.com/go-playground/validator/v10/v10.20.0/) |
| `github.com/go-viper/mapstructure/v2` | `v2.4.0` | MIT | [original files](third_party/licenses/github.com/go-viper/mapstructure/v2/v2.4.0/) |
| `github.com/goccy/go-json` | `v0.10.2` | MIT | [original files](third_party/licenses/github.com/goccy/go-json/v0.10.2/) |
| `github.com/jinzhu/inflection` | `v1.0.0` | MIT | [original files](third_party/licenses/github.com/jinzhu/inflection/v1.0.0/) |
| `github.com/jinzhu/now` | `v1.1.5` | MIT | [original files](third_party/licenses/github.com/jinzhu/now/v1.1.5/) |
| `github.com/json-iterator/go` | `v1.1.12` | MIT | [original files](third_party/licenses/github.com/json-iterator/go/v1.1.12/) |
| `github.com/klauspost/cpuid/v2` | `v2.2.7` | MIT | [original files](third_party/licenses/github.com/klauspost/cpuid/v2/v2.2.7/) |
| `github.com/leodido/go-urn` | `v1.4.0` | MIT | [original files](third_party/licenses/github.com/leodido/go-urn/v1.4.0/) |
| `github.com/mattn/go-isatty` | `v0.0.20` | MIT | [original files](third_party/licenses/github.com/mattn/go-isatty/v0.0.20/) |
| `github.com/modern-go/concurrent` | `v0.0.0-20180306012644-bacd9c7ef1dd` | Apache-2.0 | [original files](third_party/licenses/github.com/modern-go/concurrent/v0.0.0-20180306012644-bacd9c7ef1dd/) |
| `github.com/modern-go/reflect2` | `v1.0.2` | Apache-2.0 | [original files](third_party/licenses/github.com/modern-go/reflect2/v1.0.2/) |
| `github.com/pelletier/go-toml/v2` | `v2.2.4` | MIT | [original files](third_party/licenses/github.com/pelletier/go-toml/v2/v2.2.4/) |
| `github.com/sagikazarmark/locafero` | `v0.11.0` | MIT | [original files](third_party/licenses/github.com/sagikazarmark/locafero/v0.11.0/) |
| `github.com/sourcegraph/conc` | `v0.3.1-0.20240121214520-5f936abd7ae8` | MIT | [original files](third_party/licenses/github.com/sourcegraph/conc/v0.3.1-0.20240121214520-5f936abd7ae8/) |
| `github.com/spf13/afero` | `v1.15.0` | Apache-2.0 | [original files](third_party/licenses/github.com/spf13/afero/v1.15.0/) |
| `github.com/spf13/cast` | `v1.10.0` | MIT | [original files](third_party/licenses/github.com/spf13/cast/v1.10.0/) |
| `github.com/spf13/pflag` | `v1.0.10` | BSD-3-Clause | [original files](third_party/licenses/github.com/spf13/pflag/v1.0.10/) |
| `github.com/subosito/gotenv` | `v1.6.0` | MIT | [original files](third_party/licenses/github.com/subosito/gotenv/v1.6.0/) |
| `github.com/twitchyliquid64/golang-asm` | `v0.15.1` | BSD-3-Clause | [original files](third_party/licenses/github.com/twitchyliquid64/golang-asm/v0.15.1/) |
| `github.com/ugorji/go/codec` | `v1.2.12` | MIT | [original files](third_party/licenses/github.com/ugorji/go/codec/v1.2.12/) |
| `go.yaml.in/yaml/v3` | `v3.0.4` | MIT AND Apache-2.0 (file-specific) | [original files](third_party/licenses/go.yaml.in/yaml/v3/v3.0.4/) |
| `golang.org/x/arch` | `v0.8.0` | BSD-3-Clause | [original files](third_party/licenses/golang.org/x/arch/v0.8.0/) |
| `golang.org/x/crypto` | `v0.53.0` | BSD-3-Clause | [original files](third_party/licenses/golang.org/x/crypto/v0.53.0/) |
| `golang.org/x/net` | `v0.56.0` | BSD-3-Clause | [original files](third_party/licenses/golang.org/x/net/v0.56.0/) |
| `golang.org/x/sys` | `v0.46.0` | BSD-3-Clause | [original files](third_party/licenses/golang.org/x/sys/v0.46.0/) |
| `golang.org/x/text` | `v0.38.0` | BSD-3-Clause | [original files](third_party/licenses/golang.org/x/text/v0.38.0/) |
| `google.golang.org/protobuf` | `v1.36.11` | BSD-3-Clause | [original files](third_party/licenses/google.golang.org/protobuf/v1.36.11/) |
| `gopkg.in/yaml.v3` | `v3.0.1` | MIT AND Apache-2.0 (file-specific) | [original files](third_party/licenses/gopkg.in/yaml.v3/v3.0.1/) |

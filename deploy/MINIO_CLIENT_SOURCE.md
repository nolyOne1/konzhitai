# MinIO Client 源码构建记录

`Dockerfile.ops` 将 MinIO Client 的固定版本源码编译为 `/usr/bin/mc`，供现有对象存储备份命令使用。2026-09-27 核查发现原 Quay 镜像及 Docker Hub 同仓库在正常匿名 token 流程后都拒绝读取原固定 digest，因此改用官方源码构建；没有升级 mc 版本。

## 固定来源

| 项目 | 值 |
| --- | --- |
| 上游仓库 | https://github.com/minio/mc |
| 版本 | `RELEASE.2025-08-13T08-35-41Z` |
| annotated tag 对象 | `d6541ea280b73a834b64d4097e21f2be77676104` |
| 源码 commit | `7394ce0dd2a80935aded936b09fa12cbb3cb8096` |
| commit 时间 | `2025-08-13T08:35:41Z` |
| 源码归档大小 | 564228 字节 |
| 源码归档 SHA-256 | `95cd293c7119f16921a6dc515a1fb74a2227f19fd994b9c8b770a154e802ac44` |
| go.mod | Go `1.23.0`，toolchain `go1.23.10` |

固定下载地址：

<https://codeload.github.com/minio/mc/tar.gz/7394ce0dd2a80935aded936b09fa12cbb3cb8096>

官方 [tag 对象](https://api.github.com/repos/minio/mc/git/tags/d6541ea280b73a834b64d4097e21f2be77676104) 指向上述 commit；核对时 GitHub 报告 tag 和 commit 签名有效。这不是本地独立 GPG 验签。下载使用正常 TLS，归档先校验摘要再解包；471 个成员均为固定根目录下的普通文件或目录，无链接、绝对路径或父路径逃逸。

## 构建参数

构建器沿用本项目已固定 digest 的 Go 1.27.0 Alpine 镜像，设置 `GOTOOLCHAIN=local`。依赖通过 go.mod/go.sum 固定，执行 `go mod download` 与 `go mod verify`，前后检查两文件摘要；构建使用 `-mod=readonly`。

遵循官方 [Makefile](https://github.com/minio/mc/blob/7394ce0dd2a80935aded936b09fa12cbb3cb8096/Makefile) 的 `CGO_ENABLED=0`、`-trimpath` 和 `-tags kqueue`。无 `.git` 的固定归档使用 `-buildvcs=false`；依据官方 [gen-ldflags.go](https://github.com/minio/mc/blob/7394ce0dd2a80935aded936b09fa12cbb3cb8096/buildscripts/gen-ldflags.go)，显式固定以下五个 `github.com/minio/mc/cmd` 字段：

| 字段 | 值 |
| --- | --- |
| Version | `2025-08-13T08:35:41Z` |
| CopyrightYear | `2025` |
| ReleaseTag | `RELEASE.2025-08-13T08-35-41Z` |
| CommitID | `7394ce0dd2a80935aded936b09fa12cbb3cb8096` |
| ShortCommitID | `7394ce0dd2a8` |

这是同一上游源码版本的重建，不能宣称等于旧官方二进制或旧 OCI digest。新的工具链、构建参数会影响二进制字节；新镜像继续通过项目发布流水线记录 digest 和来源证明。固定归档校验失败时应停止并复核，不自动接受另一个摘要。

## 许可与发布材料

该版本随附 GNU AGPLv3；源码头注明 v3 或后续版本，NOTICE/CREDITS 记录子组件声明。最终镜像保留 `/usr/share/licenses/minio-mc/LICENSE`、`CREDITS`、`NOTICE`。对外提供构建产物时，发布材料应保留对应源码、许可声明及这里的固定来源和构建信息；不要把自构建标为官方签名二进制。上游许可原文见 [LICENSE](https://github.com/minio/mc/blob/7394ce0dd2a80935aded936b09fa12cbb3cb8096/LICENSE)。

## CI 验证

部署 CI 构建项目名固定为 `yunling-ci`，随后运行：

```sh
bash deploy/ops_mc_smoke_test.sh yunling-ci-ops
```

该检查直接使用最终 ops 镜像的默认用户，要求 UID/GID 为 `10001:10001`；验证 mc 的精确版本和 commit、许可文件及二进制 SHA。容器禁用网络、根目录只读、不挂载宿主目录，数据仅写一次性 `/tmp`；用 `mirror --overwrite --remove` 验证新文件复制、已有文件覆盖及目标多余文件移除。

部署 CI 另外复用现有 `deploy/Dockerfile.minio` 构建一次性真实 S3 fixture：

```sh
docker build -f deploy/Dockerfile.minio -t yunling-minio-smoke:ci .
bash deploy/ops_mc_s3_smoke_test.sh yunling-ci-ops yunling-minio-smoke:ci
```

S3 检查使用独立的内部 Docker 网络，不发布端口、不挂载宿主目录；凭据为本次随机生成的测试值，服务端和客户端数据均放在临时 tmpfs。最终 ops 镜像以默认 `10001:10001` 身份，通过 `MC_HOST_local` 创建 fixture bucket 和对象，再运行与 `internal/backup/export.go` 相同的 `mirror --overwrite --remove local/<bucket> <staging>`，验证添加、覆盖和目标多余文件删除，成功输出 `S3_SMOKE_OK`。退出 trap 只按创建时记录的 ID 和完整名称核对并清理本次容器与网络，不操作生产连接和数据。

现有 releaseintegration 的 `minio` 服务只是基础设施替身，因此该真实 S3 检查单独运行；部署 job 的 30 分钟超时保持不变。

本次只替换 ops 构建来源；生产 Compose 中独立的 `minio-init` 镜像仍是原配置，其新部署可用性需要独立处理。

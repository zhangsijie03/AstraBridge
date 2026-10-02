# Sub2API 上游差异核查（2026-10-02）

## 结论

星桥最新发布版为 [v0.5.17](https://github.com/zhangsijie03/AstraBridge/releases/tag/v0.5.17)，源码提交 `56e232273838b2913f0885244399d446541b25fd`。本次通过 GitHub API 查询 Sub2API 默认分支 `production`，其最新提交为 [`bf9405e4ab58c1be4fc8ec2101371753a016908e`](https://github.com/ranxi2001/sub2api/commit/bf9405e4ab58c1be4fc8ec2101371753a016908e)，与星桥 `upstream-manifest.json` 完全相同。比较结果为 `identical`，`ahead_by=0`、`behind_by=0`、`total_commits=0`。

上游最近正式 Release 仍是 [v2.9.6](https://github.com/ranxi2001/sub2api/releases/tag/v2.9.6)。[该标签至当前 production 的比较](https://github.com/ranxi2001/sub2api/compare/v2.9.6...bf9405e4ab58c1be4fc8ec2101371753a016908e) 包含 70 个提交（含合并提交），但星桥 0.5.15 已采用这个较新的 production 基线。这 70 个提交不能算成相对星桥 0.5.17 的新增更新。

## 文件核对

- 使用固定提交的完整 Git tree 核对协议目录，共 71 个文件：64 个已引入、7 个明确排除；没有未登记的新文件。
- 60 个原样 BPS 文件和 3 个传输诊断文件的 Git blob 哈希逐个与 GitHub 一致。
- 读取 4 个本地适配文件对应的上游原文，核对 Git blob 和 SHA-256；均与清单登记的上游基线一致。差异仅为 Responses 文本字段兼容和 6.1 Sol 推理强度校验及对应测试。
- 7 个排除文件属于公网图片中继及其限制/测试、平台 route 及测试。星桥继续使用原生附件上传，无需引入这些平台组件。
- 核对 v2.9.6 至当前 production 的宿主文件差异：图片忽略分支已移除；新增图片所属 Pod URL 与分布式账号并发请求 ID 属于平台部署逻辑，不适用于本地单账号网关。

## 近期上游能力与星桥对应关系

| 上游变化 | 星桥状态 |
| --- | --- |
| 原生图片优先、默认启用图片、取消静默忽略图片 | 已采用原生附件上传，相关协议文件已同步 |
| GPT-6.1 Sol 目录与推理强度校验 | 已提供请求路由与校验；真实可用性仍由当前账号的 BPS 权限决定 |
| Codex 远程模型目录、套餐识别、Astra Ultrafast 元数据 | 属于完整 Codex 平台入口；不代表 BPS 新增了对应模型权限 |
| Prism OAuth 浏览器适配与会话缓存 | 不同于 BPS 的服务端通道，依赖浏览器服务与平台账号管理 |
| API Key 配额/并发限制、账号成本倍率、Redis 候选缓存 | 多用户服务端能力；星桥已有本机 Key 校验、8 并发与请求内存预算 |
| k3s 升级、区域 Pod 路由、主副机后台采票隔离 | 服务器集群部署能力 |
| Session Studio 重登录、并行 Worker、账号状态标识 | 平台凭证运营能力；星桥继续读取 Codex 管理的当前账号 |

## 本次维护修正

修正 `unmodified_files` 将 `request.go` 和 `basispoints_test.go` 误列为原样文件的问题，并同步 README 与来源说明中的文件数量和双模型边界。来源检查现在会拒绝原样/补丁分类不一致、重复原样条目、本地文件与上游文件重叠、排除文件与已引入文件重叠，避免下次同步误覆盖兼容补丁。

本次不改变请求协议、模型路由或桌面行为，版本保持 0.5.17，不发布仅修改来源记录的新安装包。检查范围不包含未合并的实验分支，也未使用真实账号向 BPS 发起请求。已有的 `basispoints_model_access_changed` / HTTP 403 是上游模型权限拒绝，本次来源维护无法解除该权限。

## 验证

- `python3 scripts/check_upstream.py` 通过。
- 在临时副本中复现旧分类错误，并验证重复条目、本地/上游重叠、排除/引入重叠均会使来源检查失败。
- `go test -mod=vendor -race -count=1 ./internal/basispoints ./internal/gateway ./internal/modelid` 通过，网关使用本机模拟上游。
- `git diff --check` 通过。运行时代码没有改动，本次未重新打包桌面应用。

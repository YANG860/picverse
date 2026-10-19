# errmsg — 可枚举业务错误

代码：`packages/errmsg/err.go`。

保存所有**可枚举的业务错误**，使用 **4 位自定义业务码**（`common.v1.Error{code, msg}`）。

按业务域分段：

| 区段 | 域 |
| --- | --- |
| `1000–1099` | 通用（common） |
| `1100–1199` | 认证（auth） |
| `1200–1299` | 媒体（media） |
| `1300–1399` | 帖子（post） |
| `1400–1499` | 用户（user） |

## 错误码表

### common
| code | 变量 | 含义 |
| --- | --- | --- |
| 1000 | `InternalError` | 内部错误 |
| 1001 | `InvalidID` | 无效 ID |
| 1002 | `NotFound` | 资源未找到 |
| 1003 | `Unauthorized` | 未授权 |
| 1004 | `InvalidArgument` | 无效参数 |
| 1005 | `Forbidden` | 无权限 |
| 1006 | `AlreadyExist` | 资源已存在 |

### auth
| code | 变量 | 含义 |
| --- | --- | --- |
| 1101 | `InvalidEmail` | 邮箱格式非法 |
| 1102 | `InvalidPassword` | 密码不合规 |
| 1103 | `IncorrectCredential` | 账号或密码错误 |
| 1104 | `DuplicateEmail` | 邮箱已注册 |

### media
| code | 变量 | 含义 |
| --- | --- | --- |
| 1201 | `InvalidFormat` | 文件格式不支持 |

### post
| code | 变量 | 含义 |
| --- | --- | --- |
| 1301 | `InvalidTitle` | 标题非法 |
| 1302 | `InvalidContent` | 正文非法 |
| 1303 | `InvalidMedia` | 帖子媒体非法 |
| 1304 | `NotEditable` | 帖子不可编辑（非 DRAFT / 已流转，对外不区分） |
| 1305 | `Banned` | 帖子已被封禁 |

### user
| code | 变量 | 含义 |
| --- | --- | --- |
| 1401 | `InvalidName` | 昵称非法 |
| 1402 | `InvalidBio` | 简介非法 |
| 1403 | `InvalidAvatar` | 头像非法（含缺失） |

## 用法

`New(code, err)` 构造 `*commonv1.Error`，业务层以 `{Fail: true, Err: errmsg.Xxx}` 形式返回：

```go
return &authv1.LoginResponse{Fail: true, Err: errmsg.IncorrectCredential}, nil
```

约定见 [proto/proto.md](../../proto/proto.md)：`err` 必须传递可枚举、有编号的错误；无法枚举的错误直接走 gRPC 原始错误（连接层 error）。

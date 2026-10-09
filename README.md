# gooestranslate

一个用 Go 独立实现的 Windows 截图翻译工具。原生桌面界面、单个 EXE，无需 Node、Python、插件或订阅授权。

目前是 **v0.1.0 原型**，重点是打通“截图 → OCR 坐标 → 原图选字 → 翻译覆盖 → 导出图片”。

## 功能

- `Ctrl+Shift+S` 框选截图，支持虚拟桌面坐标；`Esc` 取消。
- 打开 PNG / JPEG 图片。
- 自定义 HTTP OCR：JPEG 表单上传，保留文字四角坐标。
- 点击或框选原图文字；`Ctrl` 增选，`Ctrl+A` 全选，`Ctrl+C` 复制。
- OpenAI 兼容 Chat Completions 翻译接口。
- 可选关闭模型思考：`chat_template_kwargs.enable_thinking = false`。
- 简体中文、繁体中文、英语、日语。
- 在原位置覆盖译文，原文/译文对照，PNG 导出。
- 取消请求、90 秒超时、过期请求结果隔离。
- API 密钥通过 Windows DPAPI 加密后保存在当前用户的本地设置中。

## 使用

1. 运行 `gooestranslate.exe`。
2. 在右侧填写 OCR 地址、翻译 API 基础地址、模型、密钥，点击“保存设置”。
3. 点击“截图”并框选区域，程序随即上传所选图片到 OCR 地址。也可以“打开图片”后点击“识别文字”。
4. 点击图片中的文字，或拖动框选，右侧会显示所选原文。
5. 点击“原图翻译”。翻译接口只收到识别文字，不会收到图片。
6. 取消“显示译文”可查看原图；“导出 PNG”保存当前原文/译文显示状态，不包含选择框。

应用关闭后退出进程，不驻留托盘。截图和结果只在内存中保留，除非主动导出。
设置位置：`%APPDATA%\gooestranslate\settings.json`。密钥密文通常只能由同一 Windows 用户解密。
源代码、测试和仓库不包含个人 API 密钥或私人服务配置。

## 接口协议

OCR 地址应当是可直接接收请求的完整地址，例如 `https://ocr.example.com/v1/ocr`。

```text
POST <OCR 地址>
Content-Type: multipart/form-data; boundary=...

image: uploaded_image.jpg (image/jpeg)
return_word_box: True
```

期望响应，坐标单位为上传图片的像素：

```json
[
  {"text": "Hello", "box": [[10,20],[100,20],[100,50],[10,50]]}
]
```

翻译基础地址例如 `https://api.example.com/v1`；程序在末尾添加 `/chat/completions`。已经填写完整路径也可以使用。模型需要支持非流式 Chat Completions，且能遵循逐行 JSON 输出要求。

勾选“关闭模型思考”时，请求体额外包含：

```json
{"chat_template_kwargs": {"enable_thinking": false}}
```

不支持此参数的服务应取消该选项；取消时不发送该字段。
程序不跟随接口重定向，请填写最终服务地址。

## 构建

Windows 10/11 x64，Go 1.23 或更高版本。仅使用 Go 标准库与 Windows 系统 API，不需要 C 编译器。

```powershell
go test ./...
go build -trimpath -ldflags="-s -w -H windowsgui" -o dist/gooestranslate.exe ./cmd/gooestranslate
```

隐藏窗口自测（不截图、不调用外部接口）：

```powershell
go build -o dist/gooestranslate-check.exe ./cmd/gooestranslate
./dist/gooestranslate-check.exe --smoke-test test-artifacts
```

已配置本机服务后，可显式运行真实接口测试。它只上传程序生成的测试图片，可能产生少量 API 用量：

```powershell
./dist/gooestranslate-check.exe --live-test test-artifacts
```

`test-artifacts` 内有测试图片、译文预览和结果 JSON，已在 Git 中忽略。

## 原型限制

- 当前支持一组翻译配置和一组 OCR 配置，尚无服务列表、历史记录、OCR 本地模型和托盘常驻。
- 译文采用白底覆盖与缩小字体，不做背景修复或复杂版面重排。
- 字号很小或旋转明显的截图仍可能影响译文观感；识别是否准确取决于 OCR 服务。
- 对齐以文字行为单位；原图选字使用 OCR 四角多边形命中。
- 自动测试包含协议、坐标、窗口控件、原图选字、导出与密钥加密。桌面交互和多屏混合 DPI 仍需要实际使用验收。
- EXE 尚未进行代码签名。

## 项目结构

- `internal/engine`：OCR、翻译、坐标处理与协议测试。
- `cmd/gooestranslate`：原生 Windows 窗口、截图、图片绘制、加密设置。
- `.github/workflows/build.yml`：Windows 构建与测试。

实现为独立代码，不依赖其他翻译软件的程序、插件系统或授权机制。

# gooestranslate

用 Go 实现的 Windows 桌面翻译工具。**v0.2.0** 将主窗口改为设置中心，日常通过托盘或快捷键截图，在独立结果窗口完成文字识别、翻译、原图选字、查词和朗读。

截图工具使用 Win32 绘制，设置与结果界面使用内嵌 WebView2。运行程序需要 Microsoft Edge WebView2 Runtime，无需 Node、Python 或额外插件运行环境。

界面示例（示例配置与文本）：

![设置中心](docs/screenshots/settings.png)

![原图翻译与查词](docs/screenshots/result.png)

## 开始使用

1. 运行 `gooestranslate.exe`，在设置中心填写 OCR 地址、翻译 API 基础地址、模型和密钥，保存设置。
2. 按 `Ctrl+Shift+S` 框选图片，松开鼠标后选择“识别”“翻译”或“原图翻译”。也可以在托盘右键菜单直接开始对应操作，或从设置中心打开 PNG / JPEG 图片。
3. 在结果窗口切换“文字”和“原图”视图。选中文字可以复制、查词或朗读；原图视图支持点击文字块、拖动框选及 `Ctrl` 增选。
4. 点击“翻译”查看译文；勾选“显示译文”在原图相应位置覆盖译文，点击“导出图片”保存 PNG。

关闭设置窗口默认收起到托盘，程序继续等待快捷键。双击托盘图标打开设置，右键菜单可以退出程序。关闭结果窗口只收起当前结果。

## 设置与快捷键

设置中心包含常规设置、快捷键、翻译服务、OCR、词典和朗读。支持深色、浅色和跟随系统的主题，以及识别后自动复制、翻译后自动朗读、窗口关闭后的托盘行为。

默认快捷键：

| 快捷键 | 操作 |
| --- | --- |
| `Ctrl+Shift+S` | 截图工具，执行配置的默认操作；初始为截图后选择 |
| `Ctrl+Alt+O` | 截图后识别文字 |
| `Ctrl+Alt+T` | 截图后识别并翻译文字 |
| `Ctrl+Alt+I` | 截图后识别并在原图位置显示译文 |

点击设置中的快捷键按钮，按下新组合即可录制；`Esc` 取消录制，`Backspace` 清除该快捷键。保存后立即生效，被其他程序占用的组合会显示错误。截图时按 `Esc` 取消框选。

## 识别与翻译

OCR 使用自定义 HTTP 表单上传，发送 JPEG 图片并要求返回文字四角坐标。结果窗口可以查看原文、译文和原图，支持选字、复制、查词、朗读、窗口置顶、请求取消及 PNG 导出。

翻译服务使用 OpenAI 兼容的 Chat Completions 协议，目标语言可选简体中文、繁体中文、英语、日语和韩语。勾选“关闭模型思考”会在请求体发送 `chat_template_kwargs.enable_thinking = false`。

图片只发送到配置的 OCR 地址。翻译和模型词典请求发送文字，不附带截图。本地词典查询及系统朗读使用本机资源。

## 词典与朗读

词典提供两种来源：

- **模型词典**：使用已配置的翻译模型生成释义和音标，查询词语或短语。界面会标明 AI 词典来源。
- **离线词典**：加载本机 JSON 或 ECDICT CSV 文件，按单词查询，忽略大小写，无需网络。

JSON 文件采用单词到字符串、释义数组或词条对象的映射，例如：

```json
{
  "hello": "int. 你好；喂",
  "translate": ["v. 翻译", "v. 转化"],
  "workflow": {
    "word": "workflow",
    "phonetic": "ˈwɜːkfləʊ",
    "definitions": ["n. 工作流程"]
  }
}
```

ECDICT CSV 需要 `word` 列及 `translation` 或 `definition` 列，可选 `phonetic` 列。在词典设置中填写文件路径即可使用；JSON 文件最大 32 MB，CSV 文件最大 256 MB。数据可由用户从 [ECDICT 官方项目](https://github.com/skywind3000/ECDICT)获取，应用未捆绑词库。

Manggo 内置词典使用云服务，目前未找到公开的集成接口。gooestranslate 独立实现相似的选词查词体验，并未接入或复制其云词库。

朗读使用 Windows SAPI，声音列表来自本机安装的语音。可以朗读原文、译文或所选词语，调整语速、停止朗读，或启用翻译后自动朗读。可用语言取决于 Windows 已安装的声音。

## 接口协议

OCR 地址应当是可直接接收请求的完整地址，例如 `https://ocr.example.com/v1/ocr`。

```text
POST <OCR 地址>
Content-Type: multipart/form-data; boundary=...

image: uploaded_image.jpg (image/jpeg)
return_word_box: True
```

期望响应为数组，坐标单位为上传图片的像素：

```json
[
  {"text": "Hello", "box": [[10, 20], [100, 20], [100, 50], [10, 50]]}
]
```

翻译基础地址例如 `https://api.example.com/v1`；程序会添加 `/chat/completions`，也接受包含该路径的完整地址。模型需要支持非流式 Chat Completions，并能按要求返回逐行 JSON 译文或 JSON 词条。

关闭思考时，翻译和模型词典的请求体额外包含：

```json
{"chat_template_kwargs": {"enable_thinking": false}}
```

服务不支持该参数时取消勾选即可，取消后不发送该字段。接口请求不跟随重定向，请填写最终服务地址。OCR 和翻译请求最长 90 秒，查词最长 45 秒；取消或新截图后，过期响应不会覆盖新结果。

## 本地设置

设置保存在 `%APPDATA%\gooestranslate\settings.json`，API 密钥使用 Windows DPAPI 加密，通常只能由同一 Windows 用户解密。设置中心不返回已有密钥的明文；更换密钥时填写新值并保存。

WebView2 用户数据位于同一目录的 `WebView` 子目录。个人配置和测试输出不应提交到源代码仓库。

## 构建与验证

Windows 10/11 x64，Go 1.23 或更高版本。依赖通过 Go Modules 获取，不需要 C 编译器。界面运行需要 [Microsoft Edge WebView2 Runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/#download-section)，可使用官方 Evergreen 安装程序。

```powershell
go mod download
go test ./...
go build -trimpath -ldflags="-s -w -H windowsgui" -o dist/gooestranslate.exe ./cmd/gooestranslate
```

构建带控制台输出的验证程序：

```powershell
go build -o dist/gooestranslate-check.exe ./cmd/gooestranslate
./dist/gooestranslate-check.exe --smoke-test test-artifacts
./dist/gooestranslate-check.exe --ui-smoke-test test-artifacts
```

`--smoke-test` 检查截图缓存与操作栏、译文图片导出、DPAPI 和系统声音枚举。`--ui-smoke-test` 检查实际 WebView2 界面加载、设置和结果通信、密钥字段隔离，以及原图结果推送。两者不截取真实桌面、不调用外部接口、不播放朗读音频。

配置本机服务后，可显式运行真实接口测试。它上传程序生成的测试图片，执行 OCR、翻译和模型查词，可能产生少量 API 用量：

```powershell
./dist/gooestranslate-check.exe --live-test test-artifacts
```

测试图片、译文预览和结果 JSON 写入 `test-artifacts`，该目录已在 Git 中忽略。

## 当前限制

- 支持一组翻译配置和一组 OCR 配置，尚未实现多服务列表、历史记录、生词本和本地 OCR 模型。
- 译文采用白底覆盖和字体缩小，不做背景修复或复杂版面重排；对齐以文字行为单位。
- 原图选字依赖 OCR 四角坐标；旋转文字、小字号、复杂排版和过长译文仍可能影响效果。
- 模型词典的内容由模型生成；离线词典当前按输入词语直接查询，不提供完整的词形还原或模糊搜索。
- 语音仅使用本机 SAPI 声音，不包含云端 TTS 或音频导出。
- 自动验证不能替代真实桌面框选和混合 DPI 多屏的使用验收。
- EXE 尚未进行代码签名。

## 项目结构

- `internal/engine`：OCR、翻译、词典、坐标处理与协议测试。
- `cmd/gooestranslate`：Win32 截图、WebView2 通信、托盘、快捷键、SAPI、加密设置与导出。
- `cmd/gooestranslate/ui`：设置中心与结果窗口的内嵌页面。
- `.github/workflows/build.yml`：Windows 构建与测试。

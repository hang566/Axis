# models/ — 语音模型目录

本目录存放本地语音识别模型（sherpa-onnx / SenseVoice 等），**体积较大，不入 Git 仓库**。

推荐获取方式（任选其一）：

1. **模型中心（推荐）**：启动工作台后，进入「模型中心」一键下载，带进度、断点续传，下载完成后自动生效。
   - 中文推荐 **SenseVoice**（约 200MB，中/英/日/韩/粤，自带标点）。

2. **命令行**：

   ```bash
   stt-sub models list                 # 查看可下载模型
   stt-sub models download <模型ID>    # 下载指定模型
   ```

模型目录结构（由程序自动管理）：

```
models/
└── <模型ID>/
    ├── tokens.txt
    └── model.onnx ...
```

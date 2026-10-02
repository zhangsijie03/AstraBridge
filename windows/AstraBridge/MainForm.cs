using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text.Json;

namespace AstraBridge;

internal sealed class MainForm : Form
{
    private readonly bool preview;
    private readonly EngineClient engine = new();
    private readonly UpdateChecker updateChecker = new();
    private readonly TransferLog transferLog = new();
    private readonly ToolTip tips = new();
    private readonly Label status = InterfaceStyle.Label("正在准备", 16, true);
    private readonly Label detail = InterfaceStyle.Label("正在读取本地配置…", 10, secondary: true);
    private readonly Label account = InterfaceStyle.Label("等待识别", 10);
    private readonly Label requests = InterfaceStyle.Label("0", 11, true);
    private readonly Label baseUrl = InterfaceStyle.Label("正在读取本地地址…", 11);
    private readonly Label apiKey = InterfaceStyle.Label("等待服务启动", 11);
    private readonly Label powerCaption = InterfaceStyle.Label("启动中转", 9, secondary: true);
    private readonly RelayPowerButton power = new();
    private readonly Button probe = InterfaceStyle.Button("测试连接");
    private readonly Button updateButton = InterfaceStyle.Button("检查更新");
    private readonly CopyButton copyUrl = new("复制 Base URL");
    private readonly CopyButton copyKey = new("复制 API Key");
    private readonly CopyButton copyModel = new("复制模型 ID");
    private bool busy = true;
    private bool active;
    private bool closing;
    private bool mayClose;
    private bool updateInProgress;
    private bool updateCheckInProgress;
    private string relayUrl = "";
    private string relayKey = "";

    public MainForm(bool preview)
    {
        this.preview = preview;
        Text = "AstraBridge · 星桥";
        AutoScaleMode = AutoScaleMode.Dpi;
        Font = new Font("Microsoft YaHei UI", 10);
        ClientSize = new Size(720, 940);
        MinimumSize = new Size(650, 660);
        StartPosition = FormStartPosition.CenterScreen;
        BackColor = SystemColors.Window;
        Icon = Icon.ExtractAssociatedIcon(Environment.ProcessPath!);
        BuildLayout();
        AcceptButton = power;
        power.Click += async (_, _) => await SendAsync(active ? EngineAction.Stop : EngineAction.Start);
        probe.Click += async (_, _) => await SendAsync(EngineAction.Probe);
        copyUrl.Click += (_, _) => Copy(relayUrl, copyUrl);
        copyKey.Click += (_, _) => Copy(relayKey, copyKey);
        copyModel.Click += (_, _) => Copy(Product.Model, copyModel);
        engine.Received += value => OnUi(() => Apply(value));
        engine.Faulted += message => OnUi(() =>
        {
            transferLog.Stopped(); active = false;
            if (!closing && !updateInProgress) ShowError(message);
        });
        FormClosing += CloseAsync;
        Shown += (_, _) => { InitializeEngine(); _ = CheckForUpdatesAsync(manual: false); };
        UpdateControls();
    }

    private void BuildLayout()
    {
        var root = new TableLayoutPanel
        {
            Dock = DockStyle.Fill, AutoScroll = true, ColumnCount = 1, RowCount = 7,
            Padding = new Padding(28, 20, 28, 18), BackColor = SystemColors.Window
        };
        root.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 100));
        Controls.Add(root);
        var brand = Stack();
        brand.Controls.Add(InterfaceStyle.Label("AstraBridge", 25, true));
        brand.Controls.Add(InterfaceStyle.Label("星桥 · 为 AiMaMi 连接 BPS", 10, secondary: true));
        var heading = Columns(brand, InterfaceStyle.Label(preview ? "离线预览 · 示例数据" : "", 9, secondary: true));
        Add(root, heading, 18);

        var stateText = Stack(); stateText.Controls.Add(status); stateText.Controls.Add(detail);
        detail.MaximumSize = new Size(450, 0);
        detail.MinimumSize = new Size(300, 52);
        var action = Stack(); action.Anchor = AnchorStyles.Top | AnchorStyles.Right;
        power.Anchor = AnchorStyles.None; powerCaption.Anchor = AnchorStyles.None;
        action.Controls.Add(power); action.Controls.Add(powerCaption);
        var stateRow = Columns(stateText, action); stateRow.MinimumSize = new Size(0, 104);
        Add(root, stateRow, 12);

        var sectionTitle = new FlowLayoutPanel { AutoSize = true, WrapContents = false, Dock = DockStyle.Fill, Margin = Padding.Empty };
        sectionTitle.Controls.Add(InterfaceStyle.Label("连接配置", 11, true));
        var note = InterfaceStyle.Label("  Responses · 仅限本机", 9, secondary: true);
        note.Margin = new Padding(8, 5, 0, 0); sectionTitle.Controls.Add(note);
        tips.SetToolTip(probe, "使用当前登录账号向 BPS 发送一次测试请求；每次测试后冷却 60 秒");
        Add(root, Columns(sectionTitle, probe), 8);

        var fields = new TableLayoutPanel
        {
            AutoSize = true, Dock = DockStyle.Top, ColumnCount = 1,
            BackColor = SystemColors.ControlLightLight, CellBorderStyle = TableLayoutPanelCellBorderStyle.Single,
            Padding = new Padding(1), Margin = Padding.Empty
        };
        fields.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 100));
        fields.Controls.Add(ConnectionRow("Base URL", baseUrl, copyUrl));
        fields.Controls.Add(ConnectionRow("API Key · 本地中转密钥", apiKey, copyKey));
        fields.Controls.Add(ConnectionRow("模型 ID · 固定模型 · 原生图片上传", InterfaceStyle.Label(Product.Model, 11), copyModel));
        Add(root, fields, 12);
        var count = new FlowLayoutPanel { AutoSize = true, WrapContents = false, Margin = Padding.Empty };
        count.Controls.Add(InterfaceStyle.Label("成功请求  ", 9, secondary: true)); count.Controls.Add(requests);
        account.AutoSize = false; account.AutoEllipsis = true; account.Size = new Size(360, 28);
        Add(root, Columns(account, count), 18);
        var links = new FlowLayoutPanel { AutoSize = true, WrapContents = false, FlowDirection = FlowDirection.RightToLeft, Dock = DockStyle.Fill };
        var help = InterfaceStyle.Button("接入指南"); help.Click += (_, _) => ShowHelp();
        var directory = InterfaceStyle.Button("配置目录"); directory.Click += (_, _) => OpenDirectory();
        updateButton.Click += async (_, _) => await CheckForUpdatesAsync(manual: true);
        links.Controls.Add(help); links.Controls.Add(directory);
        links.Controls.Add(updateButton);
        var guide = Stack(); guide.Controls.Add(Columns(InterfaceStyle.Label("接入 AiMaMi", 10, true), links));
        var instruction = InterfaceStyle.Label("中转注入 → 自定义中转模型，填入以上三项，协议选择 Responses。", 9, secondary: true);
        instruction.MaximumSize = new Size(610, 0); guide.Controls.Add(instruction);
        Add(root, guide, 16);
        // 主窗口剩余高度交给日志；小屏下保留整页滚动，日志内容另有独立滚动条。
        root.Controls.Add(transferLog, 0, root.RowStyles.Count);
        root.RowStyles.Add(new RowStyle(SizeType.Percent, 100));
    }

    private static TableLayoutPanel Stack() => new()
    {
        AutoSize = true, AutoSizeMode = AutoSizeMode.GrowAndShrink, ColumnCount = 1,
        Dock = DockStyle.Top, Margin = Padding.Empty, GrowStyle = TableLayoutPanelGrowStyle.AddRows
    };
    private static TableLayoutPanel Columns(Control left, Control right)
    {
        var row = new TableLayoutPanel { AutoSize = true, Dock = DockStyle.Top, ColumnCount = 2, RowCount = 1, Margin = Padding.Empty };
        row.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 100));
        row.ColumnStyles.Add(new ColumnStyle(SizeType.AutoSize));
        left.Anchor = AnchorStyles.Left | AnchorStyles.Top; right.Anchor = AnchorStyles.Right;
        row.Controls.Add(left, 0, 0); row.Controls.Add(right, 1, 0);
        return row;
    }
    private static void Add(TableLayoutPanel root, Control child, int after)
    {
        child.Margin = new Padding(0, 0, 0, after);
        root.Controls.Add(child); root.RowStyles.Add(new RowStyle(SizeType.AutoSize));
    }
    private static Control ConnectionRow(string title, Label value, CopyButton copy)
    {
        var text = Stack(); text.Controls.Add(InterfaceStyle.Label(title, 9, secondary: true));
        value.AutoEllipsis = true; value.AutoSize = false; value.Height = 28; value.Dock = DockStyle.Fill;
        value.MinimumSize = new Size(280, 28); text.Controls.Add(value);
        var row = Columns(text, copy); row.Padding = new Padding(18, 9, 14, 9);
        row.MinimumSize = new Size(0, 75); return row;
    }

    private void InitializeEngine()
    {
        if (preview)
        {
            Apply(Protocol.Read("""{"type":"state","phase":"idle","base_url":"http://127.0.0.1:17861/v1","api_key":"preview-local-key-not-a-credential","account":"demo•••@example.com","model":"gpt-6-astra","requests":0,"message":"启动本地中转后，即可通过 AiMaMi 使用固定模型。"}"""));
            return;
        }
        try { engine.Start(); }
        catch (Exception error) when (error is Win32Exception or IOException or InvalidOperationException)
        { ShowError("无法启动本地服务。请完整解压下载包，并确认 engine\\bps-local.exe 存在。"); }
    }

    private void Apply(EngineEvent value)
    {
        if (closing) return;
        if (value.Trace is { } trace) { transferLog.Append(trace); if (!active) transferLog.Stopped(); return; }
        if (value.BaseUrl is { } url) { relayUrl = url; baseUrl.Text = url; tips.SetToolTip(baseUrl, url); }
        if (value.ApiKey is { } key) { relayKey = key; apiKey.Text = "•••• •••• •••• ••••"; }
        if (value.Account is { } maskedAccount) account.Text = maskedAccount;
        requests.Text = value.Requests.ToString("N0");
        if (value.Result is { } result)
        {
            if (!active || busy) return;
            if (result.Account is { } resultAccount) account.Text = resultAccount;
            status.Text = result.Success || result.Cancelled ? "中转运行中" : result.Code switch
            {
                GatewayFailureCode.RateLimited => "最近请求被限流",
                GatewayFailureCode.ModelUnavailable => "上游模型暂不可用",
                _ => "最近请求未完成"
            };
            status.ForeColor = result.Success || result.Cancelled ? InterfaceStyle.Accent : SystemColors.ControlText;
            SetDetail(result.Success ? $"最近请求成功 · {Product.Model} · 实际推理档位 {result.Effort}" : result.Message ?? "请求未完成，请稍后重试。");
            return;
        }
        // 引擎的迟到状态不能覆盖下载进度或重新启用启停按钮。
        if (updateInProgress) return;
        if (value.Message is { } message) SetDetail(message);
        switch (value.Phase)
        {
            case EnginePhase.Idle: active = false; busy = false; status.Text = "准备就绪"; break;
            case EnginePhase.Enabled: active = true; busy = false; status.Text = "中转运行中"; break;
            case EnginePhase.Stopped: transferLog.Stopped(); active = false; busy = false; status.Text = "中转已停止"; break;
            case EnginePhase.Testing: busy = true; status.Text = "正在测试连接"; probe.Text = "测试中…"; break;
            // 探测失败不会关闭已运行的中转；保留停止按钮以便用户明确结束服务。
            case EnginePhase.Error: ShowError(value.Message ?? "操作失败"); return;
        }
        status.ForeColor = active ? InterfaceStyle.Accent : SystemColors.ControlText;
        UpdateControls();
    }
    private void SetDetail(string message) { detail.Text = message; tips.SetToolTip(detail, message); }
    private void ShowError(string message) { busy = false; status.Text = "需要检查"; status.ForeColor = SystemColors.ControlText; SetDetail(message); UpdateControls(); }
    private void UpdateControls()
    {
        power.Running = active;
        powerCaption.Text = active ? "停止中转" : "启动中转";
        power.AccessibleName = powerCaption.Text; tips.SetToolTip(power, powerCaption.Text);
        power.Enabled = probe.Enabled = !busy && !closing && !preview && !updateInProgress && engine.Available;
        updateButton.Enabled = !busy && !closing && !preview && !updateInProgress && !updateCheckInProgress;
        if (!busy) probe.Text = "测试连接";
        copyUrl.Enabled = relayUrl.Length > 0 && !closing; copyKey.Enabled = relayKey.Length > 0 && !closing;
        copyModel.Enabled = !closing;
    }
    private async Task SendAsync(EngineAction action)
    {
        if (busy || closing || preview) return;
        busy = true; UpdateControls();
        if (action == EngineAction.Probe) probe.Text = "测试中…";
        try { await engine.SendAsync(action); }
        catch (Exception error) when (error is IOException or InvalidOperationException)
        { ShowError("无法与后台通信，请重新打开应用。"); }
    }
    private void Copy(string value, CopyButton button)
    {
        if (value.Length == 0) return;
        try { Clipboard.SetText(value); button.ShowCopied(); }
        catch (ExternalException) { MessageBox.Show(this, "剪贴板暂时不可用，请稍后重试。", "暂时无法复制", MessageBoxButtons.OK, MessageBoxIcon.Information); }
    }
    private void ShowHelp() => MessageBox.Show(this,
        "1. 在 AiMaMi 登录账号，然后点击「启动中转」。\n2. 打开 AiMaMi「中转注入 → 自定义中转模型」。\n3. 填入本窗口的 Base URL、API Key、模型 ID，协议选择 Responses。\n4. 保存并启用，保持 AiMaMi 真实账号模式。\n\n每次打开星桥都需手动启动。启动仅监听本机；测试连接或发送聊天时才会访问 BPS。\n\n仅支持 gpt-6-astra。支持 PNG、JPEG、GIF、WebP 原生图片附件，单张最多 20 MiB，每次最多 20 张、合计 32 MiB。客户端需发送图片内容，不读取请求中的本地文件路径；支持 JSON / JSON Schema 输出，通过提示约束并在本地校验；不提供上游原生约束解码。\n\n默认账号目录为用户目录下的 .codex，支持继承 CODEX_HOME。代理继承 HTTPS_PROXY 环境变量。",
        "将星桥接入 AiMaMi", MessageBoxButtons.OK, MessageBoxIcon.Information);
    private async Task CheckForUpdatesAsync(bool manual)
    {
        if (preview || closing || updateInProgress || updateCheckInProgress) return;
        updateCheckInProgress = true;
        updateButton.Enabled = false;
        try
        {
            var update = await updateChecker.CheckAsync(Product.Version);
            if (closing || IsDisposed || Disposing) return;
            if (update is null)
            {
                if (manual) MessageBox.Show(this, $"当前版本 {Product.Version} 已是最新版本。", "检查更新", MessageBoxButtons.OK, MessageBoxIcon.Information);
                return;
            }
            var notes = update.Notes.Trim();
            if (notes.Length > 900) notes = notes[..900] + "…";
            if (notes.Length == 0) notes = "该版本未提供发行说明。";
            var choice = MessageBox.Show(this,
                $"发现新版本 {update.Version}。\n\n{notes}\n\n将从 GitHub 下载并校验后自动重启。",
                "AstraBridge 更新", MessageBoxButtons.YesNo, MessageBoxIcon.Information,
                MessageBoxDefaultButton.Button1);
            if (choice == DialogResult.Yes) await DownloadAndInstallAsync(update);
        }
        catch (Exception error) when (error is HttpRequestException or TaskCanceledException or InvalidDataException or FileNotFoundException or UnauthorizedAccessException or IOException or JsonException)
        {
            if (manual && !closing && !IsDisposed) MessageBox.Show(this, error.Message, "检查更新失败", MessageBoxButtons.OK, MessageBoxIcon.Warning);
        }
        finally
        {
            updateCheckInProgress = false;
            if (!updateInProgress && !closing && !IsDisposed) UpdateControls();
        }
    }

    private async Task DownloadAndInstallAsync(AppUpdate update)
    {
        if (updateInProgress || closing) return;
        updateInProgress = true; busy = true; status.Text = "正在准备更新"; SetDetail($"正在下载并校验版本 {update.Version}，请不要退出应用。"); UpdateControls();
        StagedAppUpdate? staged = null;
        try
        {
            staged = await updateChecker.StageAsync(update);
            if (closing || IsDisposed || Disposing) { updateChecker.Discard(staged); return; }
            // 目录权限问题在停止现有引擎之前报告，避免更新失败后中转也不可用。
            updateChecker.EnsureInstallLocationWritable();
            await engine.DisposeAsync();
            if (closing || IsDisposed || Disposing) { updateChecker.Discard(staged); return; }
            updateChecker.LaunchUpdater(staged);
            mayClose = true;
            Close();
        }
        catch (Exception error) when (error is HttpRequestException or TaskCanceledException or InvalidDataException or FileNotFoundException or UnauthorizedAccessException or IOException or InvalidOperationException or Win32Exception or TimeoutException)
        {
            if (staged is not null) updateChecker.Discard(staged);
            if (closing || IsDisposed || Disposing) return;
            updateInProgress = false; busy = false;
            ShowError(engine.Available ? error.Message : error.Message + " 请重新打开应用恢复中转服务。");
        }
    }
    private void OpenDirectory()
    {
        try
        {
            Directory.CreateDirectory(Product.DataDirectory);
            Process.Start(new ProcessStartInfo(Product.DataDirectory) { UseShellExecute = true });
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or Win32Exception)
        { MessageBox.Show(this, "无法打开配置目录：" + Product.DataDirectory, "打开失败", MessageBoxButtons.OK, MessageBoxIcon.Warning); }
    }
    private void OnUi(Action action)
    {
        if (IsDisposed || Disposing || !IsHandleCreated) return;
        try { BeginInvoke((Action)(() => { if (!IsDisposed) action(); })); }
        catch (InvalidOperationException) when (closing || IsDisposed) { /* 窗口已结束消息循环，无需投递过期状态。 */ }
    }
    private async void CloseAsync(object? sender, FormClosingEventArgs e)
    {
        if (mayClose) return;
        e.Cancel = true;
        if (closing) return;
        closing = true; busy = true; status.Text = "正在关闭中转…"; UpdateControls();
        // 预览或启动失败时 Dispose 可能同步完成，先离开当前 FormClosing 回调再真正关闭。
        await Task.Yield();
        try { await engine.DisposeAsync(); mayClose = true; Close(); }
        catch (Exception error) when (error is Win32Exception or IOException or InvalidOperationException or TimeoutException)
        {
            closing = false; busy = false; ShowError("后台进程未能正常退出，请在任务管理器结束 bps-local.exe 后关闭窗口。");
        }
    }
    protected override void Dispose(bool disposing)
    {
        if (disposing) { tips.Dispose(); updateChecker.Dispose(); }
        base.Dispose(disposing);
    }

    internal void VerifyPreview()
    {
        if (!preview || active || power.Enabled || probe.Enabled || !copyUrl.Enabled || !copyKey.Enabled ||
            !copyModel.Enabled || relayKey != Product.PreviewKey || status.Text != "准备就绪")
            throw new InvalidOperationException("离线预览状态不符合预期");
        if (baseUrl.Bounds.Width < 200 || power.Width < 50 || ClientSize.Width < 600)
            throw new InvalidOperationException("窗口布局尺寸不符合预期");
        if (transferLog.Parent is null || !transferLog.Visible || transferLog.FindForm() != this || transferLog.Height < 200)
            throw new InvalidOperationException("转发日志没有正确嵌入主窗口");
        updateInProgress = true; busy = true; status.Text = "正在准备更新";
        Apply(Protocol.Read("""{"type":"state","phase":"stopped","requests":0,"message":"迟到停止状态"}"""));
        if (!busy || status.Text != "正在准备更新")
            throw new InvalidOperationException("引擎状态覆盖了更新进度");
        updateInProgress = false; busy = false; status.Text = "准备就绪"; UpdateControls();
    }
}

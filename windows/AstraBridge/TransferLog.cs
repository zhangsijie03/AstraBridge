using System.Runtime.InteropServices;
using System.Text.Json.Serialization;

namespace AstraBridge;

internal enum TransferStage { Prepare, Upload, Connect, Headers, Streaming, Repair, Completed, Failed, Cancelled }
internal sealed record TransferLimitHint(string Name, string Value);
internal sealed record TransferTrace(
    [property: JsonPropertyName("request_id")] string RequestId,
    string Time, TransferStage Stage, string Message,
    [property: JsonPropertyName("elapsed_ms")] long ElapsedMs,
    [property: JsonPropertyName("quiet_ms")] long QuietMs,
    [property: JsonPropertyName("upstream_bytes")] long UpstreamBytes,
    [property: JsonPropertyName("client_events")] long ClientEvents,
    [property: JsonPropertyName("tool_calls")] long ToolCalls,
    int Attempt,
    [property: JsonPropertyName("http_status")] int? HttpStatus,
    [property: JsonPropertyName("semantic_status")] int? SemanticStatus = null,
    [property: JsonPropertyName("limit_hints")] TransferLimitHint[]? LimitHints = null)
{
    public bool Terminal => Stage is TransferStage.Completed or TransferStage.Failed or TransferStage.Cancelled;
    public string Summary => $"{RequestId} · {Message} · {ElapsedMs / 1000}s";
    public string Line => $"[{Time}] {RequestId}  {Message}\r\n  总耗时 {ElapsedMs / 1000}s · 距本轮上游数据 {QuietMs / 1000}s · 收到 {UpstreamBytes} B · 客户端事件 {ClientEvents} · 工具 {ToolCalls} · 请求次数 {Attempt}" +
        (HttpStatus is { } status ? $" · 上游 HTTP {status}" : "") +
        (SemanticStatus is { } semantic ? $" · 错误分类状态 {semantic}" : "") +
        (!Terminal && Attempt > 0 && QuietMs >= 30000 ? " · 上游暂无新数据（不代表已断开）" : "") + "\r\n" +
        (LimitHints is { Length: > 0 } hints ? "  上游限额提示（该次响应报告值，不代表限流原因）：" + string.Join(" · ", hints.Select(hint => $"{hint.Name}={hint.Value}")) + "\r\n" : "");
}

internal sealed class TransferLog : UserControl
{
    private enum Column { Outcome, Time, Request, Elapsed, Message }
    private sealed class Entry(TransferTrace trace)
    {
        public TransferTrace Trace { get; set; } = trace;
        public string Time { get; } = DateTimeOffset.TryParse(trace.Time, System.Globalization.CultureInfo.InvariantCulture,
            System.Globalization.DateTimeStyles.None, out var time) ? time.ToLocalTime().ToString("HH:mm:ss") : "--:--:--";
        public bool Interrupted { get; set; }
        public bool Terminal => Interrupted || Trace.Terminal;
        public string Glyph => Interrupted ? "—" : Trace.Stage switch
        {
            TransferStage.Completed => "✓", TransferStage.Failed => "✕", TransferStage.Cancelled => "—", _ => "…"
        };
        public string State => Interrupted ? "中转已停止" : Trace.Stage switch
        {
            TransferStage.Prepare => "校验请求", TransferStage.Upload => "上传图片", TransferStage.Connect => "连接上游",
            TransferStage.Headers => "等待响应", TransferStage.Repair => "工具纠错",
            TransferStage.Streaming => Trace.QuietMs >= 30000 ? $"等待上游 · 静默 {Trace.QuietMs / 1000}s" : "接收响应",
            TransferStage.Completed => Trace.ToolCalls > 0 ? "完成 · 已返回工具" : "完成",
            TransferStage.Cancelled => "已取消",
            TransferStage.Failed => (Trace.SemanticStatus ?? Trace.HttpStatus) switch
            {
                404 => "上游返回 404", 429 => "请求限流 · 429", 401 => "登录验证失败 · 401", 403 => "上游拒绝访问 · 403",
                504 => "等待上游超时 · 504", >= 400 and var status => $"转发失败 · {status}", _ => "转发未完成 · 悬停查看原因"
            },
            _ => "处理中"
        };
        public string Elapsed => Trace.ElapsedMs < 60000 ? $"{Trace.ElapsedMs / 1000}s" : $"{Trace.ElapsedMs / 60000}m{Trace.ElapsedMs / 1000 % 60}s";
        public string Details => (Interrupted ? "中转已停止；未确认完成。\r\n" : "") + Trace.Line;
    }
    private const int Capacity = 1000;
    private readonly Queue<string> lines = new();
    private readonly List<Entry> entries = new();
    private readonly Dictionary<string, string> active = new();
    private readonly DataGridView log = new()
    {
        Dock = DockStyle.Fill, ReadOnly = true, AllowUserToAddRows = false, AllowUserToDeleteRows = false,
        AllowUserToOrderColumns = false, AllowUserToResizeRows = false, RowHeadersVisible = false,
        MultiSelect = false, SelectionMode = DataGridViewSelectionMode.FullRowSelect,
        ScrollBars = ScrollBars.Vertical, AutoGenerateColumns = false, BackgroundColor = SystemColors.Window,
        BorderStyle = BorderStyle.FixedSingle, AutoSizeRowsMode = DataGridViewAutoSizeRowsMode.None,
        ColumnHeadersHeightSizeMode = DataGridViewColumnHeadersHeightSizeMode.AutoSize,
        CellBorderStyle = DataGridViewCellBorderStyle.SingleHorizontal
    };
    private readonly Label overview = InterfaceStyle.Label("暂无转发请求", 9);
    private readonly CheckBox follow = new() { Text = "自动滚动", Checked = true, AutoSize = true };
    private readonly Button copy = InterfaceStyle.Button("复制日志");
    private readonly Button save = InterfaceStyle.Button("导出…");
    private readonly Button clear = InterfaceStyle.Button("清空显示");
    private readonly ToolTip tips = new();
    private readonly Font outcomeFont = new("Segoe UI Symbol", 13, FontStyle.Bold);

    public TransferLog()
    {
        Dock = DockStyle.Fill; MinimumSize = new Size(0, 240); Margin = Padding.Empty;
        var root = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 1, RowCount = 4, Margin = Padding.Empty };
        root.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 100));
        root.RowStyles.Add(new RowStyle(SizeType.AutoSize)); root.RowStyles.Add(new RowStyle(SizeType.Absolute, 28));
        root.RowStyles.Add(new RowStyle(SizeType.AutoSize)); root.RowStyles.Add(new RowStyle(SizeType.Percent, 100));
        var heading = new TableLayoutPanel { AutoSize = true, Dock = DockStyle.Top, ColumnCount = 2, RowCount = 1, Margin = Padding.Empty };
        heading.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 100)); heading.ColumnStyles.Add(new ColumnStyle(SizeType.AutoSize));
        heading.Controls.Add(InterfaceStyle.Label("转发日志", 11, true), 0, 0);
        var buttons = new FlowLayoutPanel { AutoSize = true, WrapContents = false, Anchor = AnchorStyles.Right, Margin = Padding.Empty };
        follow.Font = new Font("Microsoft YaHei UI", 9); follow.Margin = new Padding(0, 8, 8, 0);
        copy.Click += (_, _) => CopyLog(); save.Click += (_, _) => SaveLog();
        clear.Click += (_, _) => ClearLog();
        follow.CheckedChanged += (_, _) => { if (follow.Checked) ScrollToLatest(); };
        tips.SetToolTip(copy, "复制最近 1000 条完整事件，包含错误原因与计数");
        buttons.Controls.AddRange(new Control[] { follow, copy, save, clear }); heading.Controls.Add(buttons, 1, 0);
        root.Controls.Add(heading, 0, 0);
        overview.AutoSize = false; overview.AutoEllipsis = true; overview.Dock = DockStyle.Fill; root.Controls.Add(overview, 0, 1);
        root.Controls.Add(InterfaceStyle.Label("每个请求一行 · 悬停查看详情 · 复制／导出保留完整事件", 9, secondary: true), 0, 2);
        log.Font = new Font("Microsoft YaHei UI", 9); log.AccessibleName = "精简转发日志"; log.Margin = new Padding(0, 6, 0, 0);
        log.RowTemplate.Height = 26; log.DefaultCellStyle.WrapMode = DataGridViewTriState.False;
        foreach (var (key, title, width) in new[] { (Column.Outcome, "结果", 42), (Column.Time, "时间", 78),
                     (Column.Request, "请求", 86), (Column.Elapsed, "耗时", 64), (Column.Message, "状态", 250) })
        {
            log.Columns.Add(new DataGridViewTextBoxColumn
            {
                Name = key.ToString(), HeaderText = title, Width = width,
                MinimumWidth = key == Column.Message ? 120 : width, Resizable = DataGridViewTriState.False,
                AutoSizeMode = key == Column.Message ? DataGridViewAutoSizeColumnMode.Fill : DataGridViewAutoSizeColumnMode.None,
                SortMode = DataGridViewColumnSortMode.NotSortable
            });
        }
        log.Columns[(int)Column.Outcome].DefaultCellStyle.Alignment = DataGridViewContentAlignment.MiddleCenter;
        log.Columns[(int)Column.Outcome].DefaultCellStyle.Font = outcomeFont;
        root.Controls.Add(log, 0, 3); Controls.Add(root); UpdateActions();
    }
    public void Append(TransferTrace value)
    {
        string? selectedId = log.CurrentRow?.Tag as string;
        int firstRow = log.IsHandleCreated ? log.FirstDisplayedScrollingRowIndex : -1;
        string? firstId = firstRow >= 0 && firstRow < entries.Count ? entries[firstRow].Trace.RequestId : null;
        if (value.Terminal) active.Remove(value.RequestId); else active[value.RequestId] = value.Summary;
        lines.Enqueue(value.Line); while (lines.Count > Capacity) lines.Dequeue();
        int row = entries.FindIndex(entry => entry.Trace.RequestId == value.RequestId);
        if (row >= 0) { entries[row].Trace = value; entries[row].Interrupted = false; }
        else
        {
            entries.Add(new Entry(value)); row = log.Rows.Add();
            if (entries.Count > Capacity)
            {
                int remove = entries.FindIndex(entry => entry.Terminal);
                if (remove < 0) remove = 0;
                entries.RemoveAt(remove); log.Rows.RemoveAt(remove); if (remove <= row) row--;
            }
        }
        if (row >= 0) UpdateRow(row);
        overview.Text = active.Count == 0 ? "暂无进行中的转发 · 最近：" + value.RequestId : $"{active.Count} 个请求转发中";
        tips.SetToolTip(overview, active.Count == 0 ? value.Summary : string.Join("\r\n", active.Values));
        // 同一请求更新原行；保留选中请求和顶部请求，避免诊断更新打断历史阅读。
        int selected = entries.FindIndex(entry => entry.Trace.RequestId == selectedId);
        if (selected >= 0) { log.CurrentCell = log.Rows[selected].Cells[0]; log.Rows[selected].Selected = true; }
        else { log.ClearSelection(); log.CurrentCell = null; }
        if (follow.Checked) ScrollToLatest();
        else if (log.IsHandleCreated && entries.Count > 0)
        {
            int top = entries.FindIndex(entry => entry.Trace.RequestId == firstId);
            log.FirstDisplayedScrollingRowIndex = top >= 0 ? top : Math.Clamp(firstRow, 0, entries.Count - 1);
        }
        UpdateActions();
    }
    private void UpdateRow(int index)
    {
        Entry entry = entries[index]; DataGridViewRow row = log.Rows[index]; row.Tag = entry.Trace.RequestId;
        row.SetValues(entry.Glyph, entry.Time, entry.Trace.RequestId, entry.Elapsed, entry.State);
        Color color = entry.Interrupted || entry.Trace.Stage == TransferStage.Cancelled ? InterfaceStyle.Secondary : entry.Trace.Stage switch
        {
            TransferStage.Completed => Color.FromArgb(30, 140, 76), TransferStage.Failed => Color.Firebrick, _ => Color.DarkGoldenrod
        };
        row.Cells[(int)Column.Outcome].Style.ForeColor = SystemInformation.HighContrast ? SystemColors.WindowText : color;
        foreach (DataGridViewCell cell in row.Cells) cell.ToolTipText = entry.Details;
    }
    public void Stopped()
    {
        active.Clear(); overview.Text = "中转未运行 · 历史日志保留在本次会话"; tips.SetToolTip(overview, null);
        for (int index = 0; index < entries.Count; index++)
            if (!entries[index].Terminal) { entries[index].Interrupted = true; UpdateRow(index); }
    }
    private void UpdateActions() => copy.Enabled = save.Enabled = clear.Enabled = lines.Count > 0;
    private void ClearLog()
    {
        lines.Clear(); entries.Clear(); log.Rows.Clear(); UpdateActions();
        // 清空显示历史不会取消正在转发的请求，其后续快照仍需显示。
        overview.Text = active.Count == 0 ? "暂无转发请求" : $"{active.Count} 个请求转发中";
        tips.SetToolTip(overview, active.Count == 0 ? null : string.Join("\r\n", active.Values));
    }
    private void ScrollToLatest()
    {
        if (log.IsHandleCreated && entries.Count > 0)
            log.FirstDisplayedScrollingRowIndex = Math.Max(0, entries.Count - Math.Max(1, log.DisplayedRowCount(false)));
    }
    private void CopyLog()
    {
        if (lines.Count == 0) return;
        try { Clipboard.SetText(string.Join("\r\n", lines)); }
        catch (ExternalException) { MessageBox.Show(FindForm(), "剪贴板暂时不可用，请重试。", "复制失败"); }
    }
    private void SaveLog()
    {
        using var picker = new SaveFileDialog { FileName = "AstraBridge-transfer.log", Filter = "日志文件|*.log", OverwritePrompt = true };
        if (picker.ShowDialog(FindForm()) != DialogResult.OK) return;
        try { File.WriteAllText(picker.FileName, string.Join("\r\n", lines)); }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException) { MessageBox.Show(FindForm(), "日志无法写入所选位置，请选择其他目录。", "导出失败"); }
    }
    protected override void Dispose(bool disposing) { if (disposing) { tips.Dispose(); outcomeFont.Dispose(); } base.Dispose(disposing); }
}

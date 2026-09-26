using System.Drawing.Drawing2D;

namespace AstraBridge;

internal static class InterfaceStyle
{
    public static Color Accent => SystemInformation.HighContrast ? SystemColors.Highlight : Color.FromArgb(17, 125, 140);
    public static Color Secondary => SystemColors.GrayText;
    public static Label Label(string text, float size = 10, bool bold = false, bool secondary = false) => new()
    {
        Text = text, AutoSize = true, Anchor = AnchorStyles.Left,
        Font = new Font("Microsoft YaHei UI", size, bold ? FontStyle.Bold : FontStyle.Regular),
        ForeColor = secondary ? Secondary : SystemColors.ControlText, Margin = new Padding(0, 3, 0, 3)
    };
    public static Button Button(string text) => new()
    {
        Text = text, AutoSize = true, AutoSizeMode = AutoSizeMode.GrowAndShrink,
        MinimumSize = new Size(88, 34), Padding = new Padding(8, 2, 8, 2), UseVisualStyleBackColor = true
    };
}

internal sealed class RelayPowerButton : Button
{
    private bool running;
    public bool Running { get => running; set { running = value; Invalidate(); } }
    public RelayPowerButton()
    {
        SetStyle(ControlStyles.UserPaint | ControlStyles.AllPaintingInWmPaint | ControlStyles.OptimizedDoubleBuffer, true);
        Size = new Size(60, 60);
        FlatStyle = FlatStyle.Flat;
        FlatAppearance.BorderSize = 0;
        AccessibleRole = AccessibleRole.PushButton;
    }
    protected override void OnPaint(PaintEventArgs e)
    {
        e.Graphics.SmoothingMode = SmoothingMode.AntiAlias;
        float diameter = Math.Min(Width, Height) - 8;
        var circle = new RectangleF((Width - diameter) / 2, (Height - diameter) / 2, diameter, diameter);
        using var fill = new SolidBrush(!Enabled ? SystemColors.ControlLight : Running ? SystemColors.Control : InterfaceStyle.Accent);
        using var border = new Pen(Enabled ? InterfaceStyle.Accent : SystemColors.GrayText);
        using var glyph = new SolidBrush(!Enabled ? SystemColors.GrayText : Running ? SystemColors.ControlText : SystemColors.HighlightText);
        e.Graphics.FillEllipse(fill, circle);
        e.Graphics.DrawEllipse(border, circle);
        float unit = diameter / 5;
        if (Running) e.Graphics.FillRectangle(glyph, Width / 2f - unit, Height / 2f - unit, unit * 2, unit * 2);
        else e.Graphics.FillPolygon(glyph, new PointF[] { new(Width / 2f - unit * .7f, Height / 2f - unit * 1.1f),
            new(Width / 2f - unit * .7f, Height / 2f + unit * 1.1f), new(Width / 2f + unit * 1.2f, Height / 2f) });
        if (Focused && ShowFocusCues) ControlPaint.DrawFocusRectangle(e.Graphics, Rectangle.Inflate(Rectangle.Round(circle), 2, 2));
    }
}

internal sealed class CopyButton : Button
{
    private readonly System.Windows.Forms.Timer feedback = new() { Interval = 1800 };
    private readonly string description;
    public CopyButton(string description)
    {
        this.description = description;
        Text = "复制";
        AccessibleName = description;
        AutoSize = true;
        MinimumSize = new Size(88, 34);
        Anchor = AnchorStyles.Right;
        feedback.Tick += (_, _) => { feedback.Stop(); Text = "复制"; AccessibleName = description; };
    }
    public void ShowCopied()
    {
        feedback.Stop(); Text = "已复制 ✓"; AccessibleName = description + "，已复制"; feedback.Start();
    }
    protected override void Dispose(bool disposing) { if (disposing) feedback.Dispose(); base.Dispose(disposing); }
}

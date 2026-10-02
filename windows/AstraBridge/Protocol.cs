using System.Reflection;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace AstraBridge;

internal static class Product
{
    public const string Name = "AstraBridge";
    public const string DefaultModel = "gpt-6-astra";
    public const string SolModel = "gpt-6.1-sol";
    // 保留 Model 别名供旧的 smoke/构建代码使用；新界面通过 Models 选择路由。
    public const string Model = DefaultModel;
    public static IReadOnlyList<string> Models => new[] { DefaultModel, SolModel };
    public static bool IsSupportedModel(string model) => Models.Contains(model, StringComparer.Ordinal);
    public const string EngineName = "bps-local.exe";
    public const string PreviewKey = "preview-local-key-not-a-credential";
    public static string Version => Assembly.GetExecutingAssembly().GetName().Version?.ToString(3) ?? "未知";
    public static string DataDirectory => Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), Name);
}

internal static class GatewayFailureCode
{
    public const string RateLimited = "basispoints_rate_limited";
    public const string ModelUnavailable = "basispoints_model_unavailable";
}

internal enum EngineAction { Start, Stop, Probe, Quit }
internal enum EnginePhase { Idle, Testing, Enabled, Stopped, Error }
internal enum EventKind { State, Request, Probe, Trace }
internal sealed record EngineCommand(EngineAction Action, string? Model = null);
internal sealed record GatewayResult(
    bool Success, bool Cancelled, string? Message, string? Model,
    string? Effort, string? Code, int? Status, string? Account, string[]? Warnings);
internal sealed record EngineEvent(
    EventKind Type, EnginePhase? Phase, string? Message, string? Account,
    string? Model, int? Port, long Requests, string? Backup,
    [property: JsonPropertyName("base_url")] string? BaseUrl,
    [property: JsonPropertyName("api_key")] string? ApiKey,
    GatewayResult? Result, TransferTrace? Trace = null);

internal static class Protocol
{
    private static readonly JsonSerializerOptions Options = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
        Converters = { new JsonStringEnumConverter(JsonNamingPolicy.CamelCase, allowIntegerValues: false) }
    };

    public static string Command(EngineAction action) => Command(action, null);
    public static string Command(EngineAction action, string? model) => JsonSerializer.Serialize(new EngineCommand(action, model), Options);

    public static EngineEvent Read(string line)
    {
        EngineEvent value = JsonSerializer.Deserialize<EngineEvent>(line, Options)
            ?? throw new JsonException("后台状态为空");
        // 拒绝缺少判别字段的事件，避免把异常输出误当成“准备就绪”。
        using JsonDocument document = JsonDocument.Parse(line);
        if (!document.RootElement.TryGetProperty("type", out _) || value.Requests < 0 ||
            (value.Type == EventKind.State && value.Phase is null) ||
            (value.Type == EventKind.Request && value.Result is null) ||
            (value.Type == EventKind.Trace && (value.Trace is null || string.IsNullOrEmpty(value.Trace.RequestId))))
            throw new JsonException("后台状态缺少必要字段");
        if (value.BaseUrl is { } url &&
            (!Uri.TryCreate(url, UriKind.Absolute, out Uri? uri) || uri.Scheme != Uri.UriSchemeHttp || uri.Host != "127.0.0.1"))
            throw new JsonException("后台地址不是本机 HTTP 地址");
        if (value.Model is { Length: > 0 } model && !Product.IsSupportedModel(model))
            throw new JsonException("后台返回的模型不受支持");
        return value;
    }
}

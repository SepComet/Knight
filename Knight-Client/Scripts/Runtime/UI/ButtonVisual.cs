#nullable enable
using Godot;

namespace SepCore.Runtime.UI;

/// <summary>Draws button state on a child panel; the parent Button owns input only.</summary>
[Tool]
public partial class ButtonVisual : Panel
{
    [Export] public StringName StyleType { get; set; } = "PrimaryButton";
    [Export] public StringName CaptionName { get; set; } = "Label";
    [Export] public StyleBox? NormalStyle { get; set; }
    [Export] public StyleBox? HoverStyle { get; set; }
    [Export] public StyleBox? PressedStyle { get; set; }
    [Export] public StyleBox? DisabledStyle { get; set; }
    [Export] public StyleBox? FocusStyle { get; set; }

    private Button _button = null!;
    private Label _label = null!;
    private Panel _focus = null!;

    public override void _Ready()
    {
        _button = GetParent<Button>();
        _label = GetNode<Label>(new NodePath(CaptionName));
        _focus = GetNode<Panel>("Focus");
        _button.Draw += Refresh;
        _button.ThemeChanged += Refresh;
        Refresh();
    }

    public override void _ExitTree()
    {
        _button.Draw -= Refresh;
        _button.ThemeChanged -= Refresh;
    }

    private void Refresh()
    {
        var state = _button.GetDrawMode();
        var styleName = state switch
        {
            BaseButton.DrawMode.Disabled => "disabled",
            BaseButton.DrawMode.Pressed or BaseButton.DrawMode.HoverPressed => "pressed",
            BaseButton.DrawMode.Hover => "hover",
            _ => "normal"
        };
        if (NormalStyle != null && _button.HasFocus() && styleName == "normal")
            styleName = "hover";
        var customStyle = styleName switch
        {
            "disabled" => DisabledStyle,
            "pressed" => PressedStyle,
            "hover" => HoverStyle,
            _ => NormalStyle
        };
        AddThemeStyleboxOverride("panel", customStyle ?? NormalStyle ?? GetThemeStylebox(styleName, StyleType));
        var colorName = styleName == "normal" ? "font_color" : $"font_{styleName}_color";
        var colorsType = HasThemeColor(colorName, StyleType) ? StyleType : new StringName("Button");
        _label.AddThemeColorOverride("font_color", GetThemeColor(colorName, colorsType));
        // Textured circular controls use their hover appearance for focus.
        // An opaque focus texture would cover the icon and the pressed state.
        _focus.Visible = _button.HasFocus() && NormalStyle == null;
        _focus.AddThemeStyleboxOverride("panel", FocusStyle ?? GetThemeStylebox("focus", StyleType));
    }
}

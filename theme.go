package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Catppuccin Latte (light) palette
// https://catppuccin.com/palette
var (
	latteBase      = color.NRGBA{R: 239, G: 241, B: 245, A: 255} // base
	latteMantle    = color.NRGBA{R: 230, G: 233, B: 239, A: 255} // mantle
	latteCrust     = color.NRGBA{R: 220, G: 224, B: 232, A: 255} // crust
	latteText      = color.NRGBA{R: 76, G: 79, B: 105, A: 255}   // text
	latteSubtext1  = color.NRGBA{R: 92, G: 95, B: 119, A: 255}   // subtext1
	latteOverlay1  = color.NRGBA{R: 178, G: 190, B: 197, A: 255} // overlay1
	latteOverlay2  = color.NRGBA{R: 166, G: 173, B: 200, A: 255} // overlay2
	lattePrimary   = color.NRGBA{R: 4, G: 165, B: 229, A: 255}   // sky
	latteSecondary = color.NRGBA{R: 104, G: 161, B: 220, A: 255} // sapphire
	latteAccent    = color.NRGBA{R: 136, G: 57, B: 239, A: 255}  // mauve
	latteGreen     = color.NRGBA{R: 64, G: 160, B: 43, A: 255}   // green
	latteRed       = color.NRGBA{R: 210, G: 15, B: 57, A: 255}   // red
)

type catppuccinLatteTheme struct{ fyne.Theme }

func NewCatppuccinLatteTheme() fyne.Theme { return &catppuccinLatteTheme{Theme: theme.LightTheme()} }

func (t *catppuccinLatteTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return latteBase
	case theme.ColorNameForeground:
		return latteText
	case theme.ColorNameDisabled:
		return latteOverlay2
	case theme.ColorNamePlaceHolder:
		return latteOverlay1
	case theme.ColorNameButton:
		return latteMantle
	case theme.ColorNamePrimary:
		return lattePrimary
	case theme.ColorNameFocus, theme.ColorNameSelection:
		return latteSecondary
	case theme.ColorNameHover:
		return color.NRGBA{R: 210, G: 224, B: 239, A: 255}
	case theme.ColorNameInputBorder, theme.ColorNameSeparator, theme.ColorNameShadow:
		return latteCrust
	default:
		return t.Theme.Color(name, variant)
	}
}

func (t *catppuccinLatteTheme) Icon(name fyne.ThemeIconName) fyne.Resource { return t.Theme.Icon(name) }
func (t *catppuccinLatteTheme) Font(style fyne.TextStyle) fyne.Resource    { return t.Theme.Font(style) }
func (t *catppuccinLatteTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 6
	case theme.SizeNameInlineIcon:
		return 20
	case theme.SizeNameScrollBar:
		return 14
	case theme.SizeNameScrollBarSmall:
		return 10
	case theme.SizeNameText:
		return 13
	case theme.SizeNameInputBorder:
		return 1
	default:
		return t.Theme.Size(name)
	}
}

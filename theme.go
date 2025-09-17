
package main

import (
    "image/color"

    "fyne.io/fyne/v2"
    "fyne.io/fyne/v2/theme"
)

// Catppuccin Latte (light) palette
// Reference: https://catppuccin.com/palette
// Core tones
var (
    ctpBase      = color.NRGBA{R: 0xEF, G: 0xF1, B: 0xF5, A: 0xFF} // base
    ctpMantle    = color.NRGBA{R: 0xE6, G: 0xE9, B: 0xEF, A: 0xFF} // mantle
    ctpCrust     = color.NRGBA{R: 0xDC, G: 0xE0, B: 0xE8, A: 0xFF} // crust
    ctpText      = color.NRGBA{R: 0x4C, G: 0x4F, B: 0x69, A: 0xFF} // text
    ctpOverlay0  = color.NRGBA{R: 0xDC, G: 0xE0, B: 0xE8, A: 0xFF} // overlay0
    ctpOverlay1  = color.NRGBA{R: 0xCC, G: 0xD0, B: 0xDA, A: 0xFF} // overlay1
    ctpOverlay2  = color.NRGBA{R: 0xBC, G: 0xC0, B: 0xCC, A: 0xFF} // overlay2
    ctpPrimary   = color.NRGBA{R: 0x1E, G: 0x66, B: 0xF5, A: 0xFF} // blue
    ctpSky       = color.NRGBA{R: 0x04, G: 0xA5, B: 0xE5, A: 0xFF} // sky (focus/selection)
)

type catppuccinLatteTheme struct{ fyne.Theme }

func NewCatppuccinLatteTheme() fyne.Theme { return &catppuccinLatteTheme{Theme: theme.LightTheme()} }

func (t *catppuccinLatteTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
    switch name {
    case theme.ColorNameBackground:
        return ctpBase
    case theme.ColorNameButton:
        return ctpMantle
    case theme.ColorNamePrimary:
        return ctpPrimary
    case theme.ColorNameHover:
        return ctpOverlay0
    case theme.ColorNameFocus, theme.ColorNameSelection:
        return ctpSky
    case theme.ColorNameShadow:
        return ctpCrust
    case theme.ColorNameInputBorder:
        return ctpOverlay1
    case theme.ColorNameDisabled, theme.ColorNamePlaceHolder:
        return ctpOverlay2
    case theme.ColorNameForeground:
        return ctpText
    case theme.ColorNameSeparator:
        return ctpOverlay1
    default:
        return t.Theme.Color(name, variant)
    }
}

func (t *catppuccinLatteTheme) Icon(name fyne.ThemeIconName) fyne.Resource { return t.Theme.Icon(name) }
func (t *catppuccinLatteTheme) Font(style fyne.TextStyle) fyne.Resource    { return t.Theme.Font(style) }

func (t *catppuccinLatteTheme) Size(name fyne.ThemeSizeName) float32 {
    switch name {
    case theme.SizeNameInputBorder:
        return 1
    default:
        return t.Theme.Size(name)
    }
}

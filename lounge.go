package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	userDataFile     = "log/active_users.json"
	deviceLayoutFile = "log/device_layout.json"
	memberFile       = "membership.csv"
	logDir           = "log"
	imgBaseDir       = "src"

	pcImageSize float32 = 48
)

type User struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	CheckInTime time.Time `json:"checkin_time"`
	PCID        int       `json:"pc_id"`
}

type Device struct {
	ID     int
	Type   string
	Status string
	UserID string
}

type Member struct {
	Name          string
	ID            string
	Email         string
	StudentNumber string
	PhoneNumber   string
}

type LogEntry struct {
	UserName     string    `json:"user_name"`
	UserID       string    `json:"user_id"`
	PCID         int       `json:"pc_id"`
	CheckInTime  time.Time `json:"check_in_time"`
	CheckOutTime time.Time `json:"check_out_time,omitempty"`
	UsageTime    string    `json:"usage_time,omitempty"`
}

var (
	allDevices                []Device
	activeUsers               []User
	members                   []Member
	mainWindow                fyne.Window
	logTable                  *widget.Table
	refreshTrigger            = make(chan bool, 1)
	logRefreshPending         = false
	logFileMutex              sync.Mutex
	currentLogEntries         []LogEntry
	tabs                      *container.AppTabs
	deviceStatusTabIndex      int = -1
	logTabIndex               int = -1
	deviceHoverDetailLabel    *widget.Label
	activeUserNetworkInstance *ActiveUserNetworkWidget
)

type classicTheme struct{ fyne.Theme }

func newClassicTheme() fyne.Theme { return &classicTheme{Theme: theme.LightTheme()} }

func (themeInstance *classicTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	classicBackground := color.NRGBA{R: 0xC0, G: 0xC0, B: 0xC0, A: 0xFF}
	classicButtonFace := color.NRGBA{R: 0xD4, G: 0xD0, B: 0xC8, A: 0xFF}
	classicText := color.Black
	classicPrimary := color.NRGBA{R: 0x00, G: 0x00, B: 0x80, A: 0xFF}
	classicDisabled := color.NRGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xFF}
	classicInputBorder := color.NRGBA{R: 0x40, G: 0x40, B: 0x40, A: 0xFF}
	classicShadow := color.NRGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xFF}

	lightBlue := color.NRGBA{R: 0x66, G: 0x9C, B: 0xFF, A: 0xFF}

	switch name {
	case theme.ColorNameBackground:
		return classicBackground
	case theme.ColorNameButton:
		return classicButtonFace
	case theme.ColorNamePrimary:
		return classicPrimary
	case theme.ColorNameHover:
		return color.NRGBA{R: 0xB0, G: 0xB0, B: 0xD0, A: 0xFF}
	case theme.ColorNameFocus, theme.ColorNameSelection:
		return lightBlue
	case theme.ColorNameShadow:
		return classicShadow
	case theme.ColorNameInputBorder:
		return classicInputBorder
	case theme.ColorNameDisabled, theme.ColorNamePlaceHolder:
		return classicDisabled
	case theme.ColorNameForeground:
		return classicText
	case theme.ColorNameSeparator:
		return classicShadow
	default:
		return themeInstance.Theme.Color(name, variant)
	}
}

func (themeInstance *classicTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return themeInstance.Theme.Icon(name)
}

func (themeInstance *classicTheme) Font(style fyne.TextStyle) fyne.Resource {
	return themeInstance.Theme.Font(style)
}

func (themeInstance *classicTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 4
	case theme.SizeNameInlineIcon:
		return 20
	case theme.SizeNameScrollBar:
		return 16
	case theme.SizeNameScrollBarSmall:
		return 12
	case theme.SizeNameText:
		return 12
	case theme.SizeNameInputBorder:
		return 1
	default:
		return themeInstance.Theme.Size(name)
	}
}

// ActiveUserNetworkWidget renders a vertical, scrollable queue of active users.
type ActiveUserNetworkWidget struct {
	widget.BaseWidget
	users            []User
	pcPositions      map[string]fyne.Position
	busyPCImage      fyne.Resource
	consoleBusyImage fyne.Resource
	list             *widget.List
}

func NewActiveUserNetworkWidget() *ActiveUserNetworkWidget {
	widgetInstance := &ActiveUserNetworkWidget{
		users:       make([]User, 0),
		pcPositions: make(map[string]fyne.Position),
	}

	busyPCPath := filepath.Join(imgBaseDir, "busy.png")
	if _, err := os.Stat(busyPCPath); err == nil {
		widgetInstance.busyPCImage, _ = fyne.LoadResourceFromPath(busyPCPath)
	} else {
		widgetInstance.busyPCImage = theme.ComputerIcon()
	}

	consoleBusyPath := filepath.Join(imgBaseDir, "console_busy.png")
	if _, err := os.Stat(consoleBusyPath); err == nil {
		widgetInstance.consoleBusyImage, _ = fyne.LoadResourceFromPath(consoleBusyPath)
	} else {
		widgetInstance.consoleBusyImage = widgetInstance.busyPCImage
	}

	widgetInstance.ExtendBaseWidget(widgetInstance)
	return widgetInstance
}

func (widgetInstance *ActiveUserNetworkWidget) UpdateUsers(updatedUsers []User) {
	widgetInstance.users = append([]User(nil), updatedUsers...)
	if widgetInstance.list != nil {
		widgetInstance.list.Refresh()
	}
	widgetInstance.Refresh()
}

type activeUserNetworkRenderer struct {
	widget  *ActiveUserNetworkWidget
	objects []fyne.CanvasObject
}

// buildListItem constructs one compact row with a fixed-size icon and a text column.
func (rendererInstance *activeUserNetworkRenderer) buildListItem() fyne.CanvasObject {
	imageWidget := canvas.NewImageFromResource(rendererInstance.widget.busyPCImage)
	imageWidget.FillMode = canvas.ImageFillContain
	imageWidget.SetMinSize(fyne.NewSize(pcImageSize, pcImageSize))

	deviceLabel := widget.NewLabel("")
	nameLabel := widget.NewLabel("")
	nameLabel.TextStyle = fyne.TextStyle{Bold: true}
	idLabel := widget.NewLabel("")
	inLabel := widget.NewLabel("")
	upLabel := widget.NewLabel("")

	textColumn := container.NewVBox(deviceLabel, nameLabel, idLabel, inLabel, upLabel)
	row := container.NewHBox(imageWidget, textColumn)
	return row
}

// bindListItem fills a row with user data.
func (rendererInstance *activeUserNetworkRenderer) bindListItem(item fyne.CanvasObject, user User) {
	row := item.(*fyne.Container)
	imageWidget := row.Objects[0].(*canvas.Image)
	textColumn := row.Objects[1].(*fyne.Container)

	deviceLabel := textColumn.Objects[0].(*widget.Label)
	nameLabel := textColumn.Objects[1].(*widget.Label)
	idLabel := textColumn.Objects[2].(*widget.Label)
	inLabel := textColumn.Objects[3].(*widget.Label)
	upLabel := textColumn.Objects[4].(*widget.Label)

	device := getDeviceByID(user.PCID)
	imageResource := rendererInstance.widget.busyPCImage
	if device != nil && device.Type == "Console" && rendererInstance.widget.consoleBusyImage != nil {
		imageResource = rendererInstance.widget.consoleBusyImage
	}
	imageWidget.Resource = imageResource
	imageWidget.Refresh()

	deviceLabel.SetText(fmt.Sprintf("PC: %d", user.PCID))
	nameLabel.SetText(user.Name)
	idLabel.SetText(fmt.Sprintf("ID: %s", user.ID))
	inLabel.SetText(fmt.Sprintf("In: %s", user.CheckInTime.Format("15:04:05")))
	upLabel.SetText(fmt.Sprintf("Up: %s", getFormattedUsageDuration(user.CheckInTime)))
}

func (rendererInstance *activeUserNetworkRenderer) Layout(size fyne.Size) {
	rendererInstance.objects[0].Resize(size)
}

func (rendererInstance *activeUserNetworkRenderer) MinSize() fyne.Size {
	return fyne.NewSize(320, 300)
}

func (rendererInstance *activeUserNetworkRenderer) Refresh() {
	if rendererInstance.widget.list != nil {
		rendererInstance.widget.list.Refresh()
	}
	canvas.Refresh(rendererInstance.widget)
}

func (rendererInstance *activeUserNetworkRenderer) Objects() []fyne.CanvasObject {
	return rendererInstance.objects
}

// Destroy satisfies fyne.WidgetRenderer; no resources to release.
func (rendererInstance *activeUserNetworkRenderer) Destroy() {}

func (widgetInstance *ActiveUserNetworkWidget) CreateRenderer() fyne.WidgetRenderer {
	if widgetInstance.list == nil {
		rendererInstance := &activeUserNetworkRenderer{widget: widgetInstance}
		widgetInstance.list = widget.NewList(
			func() int { return len(widgetInstance.users) },
			func() fyne.CanvasObject { return rendererInstance.buildListItem() },
			func(index widget.ListItemID, item fyne.CanvasObject) {
				if index >= 0 && index < len(widgetInstance.users) {
					rendererInstance.bindListItem(item, widgetInstance.users[index])
				}
			},
		)
		rendererInstance.objects = []fyne.CanvasObject{widgetInstance.list}
		return rendererInstance
	}
	return &activeUserNetworkRenderer{widget: widgetInstance, objects: []fyne.CanvasObject{widgetInstance.list}}
}

type DeviceStatusLayoutWidget struct {
	widget.BaseWidget
	containerSize    fyne.Size
	slotPositions    []fyne.Position
	deviceToSlot     map[int]int
	draggingDeviceID int
	dragOffset       fyne.Position
	isDragging       bool
	transientDragPos fyne.Position
	pcIconSize       float32
	consoleIconSize  float32
	slotSpacingX     float32
	slotSpacingY     float32
	slotMargin       float32
}

func NewDeviceStatusLayoutWidget() *DeviceStatusLayoutWidget {
	w := &DeviceStatusLayoutWidget{
		deviceToSlot:    make(map[int]int),
		pcIconSize:      64,
		consoleIconSize: 64,
		slotSpacingX:    110,
		slotSpacingY:    110,
		slotMargin:      24,
	}
	w.loadDeviceLayout()
	w.ExtendBaseWidget(w)
	return w
}

func (w *DeviceStatusLayoutWidget) defaultOrder() []int {
	return []int{16, 15, 14, 11, 12, 13, 10, 9, 8, 7, 6, 5, 1, 2, 3, 4, 17, 18}
}

func (w *DeviceStatusLayoutWidget) ensureMapping() {
	if len(w.deviceToSlot) == len(allDevices) {
		return
	}
	seen := make(map[int]bool)
	for deviceID := range w.deviceToSlot {
		seen[deviceID] = true
	}
	order := w.defaultOrder()
	slot := 0
	occupiedSlots := make(map[int]bool)
	for _, s := range w.deviceToSlot {
		occupiedSlots[s] = true
	}
	for _, d := range order {
		if !seen[d] {
			for {
				if !occupiedSlots[slot] {
					w.deviceToSlot[d] = slot
					occupiedSlots[slot] = true
					slot++
					break
				}
				slot++
			}
		}
	}
}

func (w *DeviceStatusLayoutWidget) loadDeviceLayout() {
	_ = ensureLogDir()
	b, err := os.ReadFile(deviceLayoutFile)
	if err != nil || len(b) == 0 {
		w.deviceToSlot = make(map[int]int)
		w.ensureMapping()
		w.saveDeviceLayout()
		return
	}
	type entry struct{ DeviceID, Slot int }
	var entries []entry
	if json.Unmarshal(b, &entries) != nil {
		w.deviceToSlot = make(map[int]int)
		w.ensureMapping()
		w.saveDeviceLayout()
		return
	}
	w.deviceToSlot = make(map[int]int)
	for _, e := range entries {
		w.deviceToSlot[e.DeviceID] = e.Slot
	}
	w.ensureMapping()
}

func (w *DeviceStatusLayoutWidget) saveDeviceLayout() {
	type entry struct{ DeviceID, Slot int }
	entries := make([]entry, 0, len(w.deviceToSlot))
	for id, s := range w.deviceToSlot {
		entries = append(entries, entry{DeviceID: id, Slot: s})
	}
	data, _ := json.MarshalIndent(entries, "", "  ")
	_ = os.WriteFile(deviceLayoutFile, data, 0o644)
}

func (w *DeviceStatusLayoutWidget) computeSlots() {
	// build 16 PC slots as a centered grid on the left, then 2 console slots on the right
	if w.containerSize.IsZero() {
		return
	}
	w.slotPositions = w.slotPositions[:0]

	totalSlots := len(allDevices)
	leftWidth := w.containerSize.Width * 0.75
	leftX := w.slotMargin
	topY := w.slotMargin

	rowHeights := []int{3, 3, 3, 3, 4}
	rows := len(rowHeights)

	placed := 0
	for r := 0; r < rows && placed < 16 && placed < totalSlots; r++ {
		cols := rowHeights[r]
		rowY := topY + float32(r)*w.slotSpacingY
		rowWidth := float32(cols-1) * w.slotSpacingX
		startX := leftX + (leftWidth-rowWidth)/2
		for c := 0; c < cols && placed < 16 && placed < totalSlots; c++ {
			w.slotPositions = append(w.slotPositions, fyne.NewPos(startX+float32(c)*w.slotSpacingX, rowY))
			placed++
		}
	}

	if placed < totalSlots {
		rightX := leftWidth + w.slotMargin*2
		y1 := topY + 1*w.slotSpacingY
		y2 := topY + 3*w.slotSpacingY
		w.slotPositions = append(w.slotPositions, fyne.NewPos(rightX, y1))
		if len(w.slotPositions) < totalSlots {
			w.slotPositions = append(w.slotPositions, fyne.NewPos(rightX, y2))
		}
	}

	for len(w.slotPositions) < totalSlots {
		w.slotPositions = append(w.slotPositions, fyne.NewPos(leftX, topY))
	}
}

func (w *DeviceStatusLayoutWidget) UpdateDevices() {
	w.ensureMapping()
	w.Refresh()
}

func (w *DeviceStatusLayoutWidget) Tapped(ev *fyne.PointEvent) {
	for _, d := range allDevices {
		center := w.positionForDevice(d.ID)
		size := w.iconSizeForDevice(d.ID)
		topLeft := fyne.NewPos(center.X-size/2, center.Y-size/2)
		if ev.Position.X >= topLeft.X && ev.Position.X <= topLeft.X+size &&
			ev.Position.Y >= topLeft.Y && ev.Position.Y <= topLeft.Y+size {

			if d.Status == "occupied" {
				if d.Type == "Console" {
					userIDs := activeUserIDsOnDevice(d.ID)
					if len(userIDs) == 0 {
						return
					}
					display := []string{}
					for _, id := range userIDs {
						u := getUserByID(id)
						name := "Unknown User"
						if u != nil {
							name = u.Name
						}
						display = append(display, fmt.Sprintf("%s (ID: %s)", name, id))
					}
					selector := widget.NewSelectEntry(display)
					formItems := []*widget.FormItem{{Text: "User on " + d.Type, Widget: selector}}
					dialogInstance := dialog.NewForm("Checkout From "+d.Type, "Check Out", "Cancel", formItems, func(confirmed bool) {
						if !confirmed {
							return
						}
						choice := strings.TrimSpace(selector.Text)
						if choice == "" {
							return
						}
						var targetID string
						for i, s := range display {
							if s == choice {
								targetID = userIDs[i]
								break
							}
						}
						if targetID == "" {
							return
						}
						if err := checkoutUser(targetID); err != nil {
							dialog.ShowError(err, mainWindow)
						}
					}, mainWindow)
					dialogInstance.Resize(fyne.NewSize(420, dialogInstance.MinSize().Height))
					dialogInstance.Show()
					return
				}

				user := getUserByID(d.UserID)
				userName := "Unknown User"
				if user != nil {
					userName = user.Name
				}
				dialog.ShowConfirm("Confirm Checkout", fmt.Sprintf("Checkout %s from %s %d?", userName, d.Type, d.ID),
					func(confirmed bool) {
						if confirmed {
							if err := checkoutUser(d.UserID); err != nil {
								dialog.ShowError(err, mainWindow)
							}
						}
					}, mainWindow)
			} else {
				showCheckInDialogShared(d.ID, true)
			}
			return
		}
	}
}

func (w *DeviceStatusLayoutWidget) Dragged(ev *fyne.DragEvent) {
	if !w.isDragging {
		for _, d := range allDevices {
			center := w.positionForDevice(d.ID)
			size := w.iconSizeForDevice(d.ID)
			topLeft := fyne.NewPos(center.X-size/2, center.Y-size/2)
			if ev.Position.X >= topLeft.X && ev.Position.X <= topLeft.X+size &&
				ev.Position.Y >= topLeft.Y && ev.Position.Y <= topLeft.Y+size {
				w.isDragging = true
				w.draggingDeviceID = d.ID
				w.dragOffset = fyne.NewPos(ev.Position.X-center.X, ev.Position.Y-center.Y)
				w.transientDragPos = center
				break
			}
		}
	}
	if w.isDragging && w.draggingDeviceID != 0 {
		newX := ev.Position.X - w.dragOffset.X
		newY := ev.Position.Y - w.dragOffset.Y
		minX := w.slotMargin + w.pcIconSize/2
		maxX := w.containerSize.Width - w.slotMargin - w.pcIconSize/2
		minY := w.slotMargin + w.pcIconSize/2
		maxY := w.containerSize.Height - w.slotMargin - w.pcIconSize/2
		if newX < minX {
			newX = minX
		}
		if newX > maxX {
			newX = maxX
		}
		if newY < minY {
			newY = minY
		}
		if newY > maxY {
			newY = maxY
		}
		w.transientDragPos = fyne.NewPos(newX, newY)
		w.Refresh()
	}
}

func (w *DeviceStatusLayoutWidget) DragEnd() {
	if !w.isDragging || w.draggingDeviceID == 0 {
		w.isDragging = false
		w.draggingDeviceID = 0
		return
	}
	targetSlot := w.nearestSlot(w.transientDragPos)
	if targetSlot >= 0 {
		currentSlot := w.deviceToSlot[w.draggingDeviceID]
		if currentSlot != targetSlot {
			otherID := -1
			for id, s := range w.deviceToSlot {
				if s == targetSlot {
					otherID = id
					break
				}
			}
			w.deviceToSlot[w.draggingDeviceID] = targetSlot
			if otherID != -1 {
				w.deviceToSlot[otherID] = currentSlot
			}
			w.saveDeviceLayout()
		}
	}
	w.isDragging = false
	w.draggingDeviceID = 0
	w.Refresh()
}

func (w *DeviceStatusLayoutWidget) positionForDevice(deviceID int) fyne.Position {
	w.ensureMapping()
	slot := w.deviceToSlot[deviceID]
	if slot >= 0 && slot < len(w.slotPositions) {
		if w.isDragging && w.draggingDeviceID == deviceID {
			return w.transientDragPos
		}
		return w.slotPositions[slot]
	}
	if len(w.slotPositions) == 0 {
		return fyne.NewPos(w.slotMargin+w.pcIconSize, w.slotMargin+w.pcIconSize)
	}
	index := slot % len(w.slotPositions)
	return w.slotPositions[index]
}

func (w *DeviceStatusLayoutWidget) iconSizeForDevice(deviceID int) float32 {
	if deviceID == 17 || deviceID == 18 {
		return w.consoleIconSize
	}
	return w.pcIconSize
}

type deviceStatusRenderer struct {
	widget  *DeviceStatusLayoutWidget
	objects []fyne.CanvasObject
}

func (r *deviceStatusRenderer) Layout(size fyne.Size) {
	if r.widget.containerSize != size {
		r.widget.containerSize = size
		r.widget.computeSlots()
	}
	r.widget.Refresh()
}

func (r *deviceStatusRenderer) MinSize() fyne.Size {
	return fyne.NewSize(700, 420)
}

func (r *deviceStatusRenderer) Refresh() {
	r.objects = r.objects[:0]
	for _, d := range allDevices {
		center := r.widget.positionForDevice(d.ID)
		size := r.widget.iconSizeForDevice(d.ID)

		var baseImageName string
		if d.Status == "free" {
			if d.Type == "PC" {
				baseImageName = "free.png"
			} else {
				baseImageName = "console.png"
			}
		} else {
			if d.Type == "PC" {
				baseImageName = "busy.png"
			} else {
				baseImageName = "console_busy.png"
			}
		}
		imagePath := filepath.Join(imgBaseDir, baseImageName)
		icon := canvas.NewImageFromFile(imagePath)
		icon.FillMode = canvas.ImageFillContain
		icon.SetMinSize(fyne.NewSize(size, size))
		icon.Resize(fyne.NewSize(size, size))
		icon.Move(fyne.NewPos(center.X-size/2, center.Y-size/2))
		r.objects = append(r.objects, icon)

		labelText := strconv.Itoa(d.ID)
		if d.ID == 17 {
			labelText = "Xbox " + labelText
		}
		if d.ID == 18 {
			labelText = "PS4 " + labelText
		}
		label := canvas.NewText(labelText, theme.ForegroundColor())
		label.TextStyle.Bold = true
		label.Alignment = fyne.TextAlignCenter
		label.TextSize = 12
		label.Move(fyne.NewPos(center.X-label.MinSize().Width/2, center.Y+size/2-4))
		r.objects = append(r.objects, label)
	}
	canvas.Refresh(r.widget)
}

func (r *deviceStatusRenderer) Objects() []fyne.CanvasObject { return r.objects }
func (r *deviceStatusRenderer) Destroy()                     {}

func (w *DeviceStatusLayoutWidget) CreateRenderer() fyne.WidgetRenderer {
	w.computeSlots()
	return &deviceStatusRenderer{widget: w}
}

func (w *DeviceStatusLayoutWidget) nearestSlot(p fyne.Position) int {
	if len(w.slotPositions) == 0 {
		return -1
	}
	best := -1
	bestDist := math.MaxFloat64
	for i, s := range w.slotPositions {
		dx := float64(s.X - p.X)
		dy := float64(s.Y - p.Y)
		dist := dx*dx + dy*dy
		if dist < bestDist {
			bestDist = dist
			best = i
		}
	}
	return best
}

func ensureLogDir() error { return os.MkdirAll(logDir, 0o755) }

func getLogFilePath() string {
	today := time.Now().Format("2006-01-02")
	return filepath.Join(logDir, fmt.Sprintf("lounge-%s.json", today))
}

func readDailyLogEntries() ([]LogEntry, error) {
	filePath := getLogFilePath()
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return []LogEntry{}, nil
	}
	logFile, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("error opening log: %s: %w", filePath, err)
	}
	defer logFile.Close()
	logFileBytes, err := io.ReadAll(logFile)
	if err != nil {
		return nil, fmt.Errorf("error reading log: %s: %w", filePath, err)
	}
	var entries []LogEntry
	if len(logFileBytes) > 0 {
		if err := json.Unmarshal(logFileBytes, &entries); err != nil {
			return nil, fmt.Errorf("error unmarshaling log: %s: %w", filePath, err)
		}
	}
	return entries, nil
}

func writeDailyLogEntries(entries []LogEntry) error {
	filePath := getLogFilePath()
	fileData, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling log: %w", err)
	}
	return os.WriteFile(filePath, fileData, 0o644)
}

func recordLogEvent(isCheckIn bool, user User, deviceID int, originalCheckInTimeForUpdate *time.Time) {
	logFileMutex.Lock()
	defer logFileMutex.Unlock()
	if err := ensureLogDir(); err != nil {
		fmt.Println("Error creating log directory:", err)
		return
	}
	entries, err := readDailyLogEntries()
	if err != nil {
		fmt.Println("Error reading daily log for update:", err)
		return
	}
	if isCheckIn {
		entries = append(entries, LogEntry{UserName: user.Name, UserID: user.ID, PCID: deviceID, CheckInTime: user.CheckInTime})
	} else {
		found := false
		for entryIndex := len(entries) - 1; entryIndex >= 0; entryIndex-- {
			entry := entries[entryIndex]
			if entry.UserID == user.ID && entry.PCID == deviceID && entry.CheckOutTime.IsZero() {
				matchTime := originalCheckInTimeForUpdate == nil || entry.CheckInTime.Equal(*originalCheckInTimeForUpdate)
				if matchTime {
					entries[entryIndex].CheckOutTime = time.Now()
					entries[entryIndex].UsageTime = formatDuration(entries[entryIndex].CheckOutTime.Sub(entries[entryIndex].CheckInTime))
					found = true
					break
				}
			}
		}
		if !found {
			fmt.Printf("Error: No matching check-in for user %s (ID: %s) Device %d.\n", user.Name, user.ID, deviceID)
		}
	}
	if err := writeDailyLogEntries(entries); err != nil {
		fmt.Println("Error writing daily log:", err)
	}
	fyne.Do(func() {
		currentLogEntries = entries
		if tabs != nil && tabs.Selected() != nil && tabs.Selected().Text == "Log" {
			if logTable != nil {
				logTable.Refresh()
			}
		} else {
			logRefreshPending = true
		}
	})
}

func formatDuration(durationValue time.Duration) string {
	durationValue = durationValue.Round(time.Second)
	hours := int(durationValue.Hours())
	minutes := int(durationValue.Minutes()) % 60
	seconds := int(durationValue.Seconds()) % 60
	if hours > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

func updateCurrentLogEntriesCache() {
	entries, err := readDailyLogEntries()
	if err != nil {
		fmt.Println("Error updating log cache:", err)
		currentLogEntries = []LogEntry{}
	} else {
		currentLogEntries = entries
	}
}

func buildLogView() fyne.CanvasObject {
	updateCurrentLogEntriesCache()
	logTable = widget.NewTable(
		func() (int, int) { return len(currentLogEntries) + 1, 6 },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(cellID widget.TableCellID, cellObject fyne.CanvasObject) {
			label := cellObject.(*widget.Label)
			if cellID.Row == 0 {
				label.TextStyle.Bold = true
				switch cellID.Col {
				case 0:
					label.SetText("User Name")
				case 1:
					label.SetText("User ID")
				case 2:
					label.SetText("Device ID")
				case 3:
					label.SetText("Checked In")
				case 4:
					label.SetText("Checked Out")
				case 5:
					label.SetText("Usage Time")
				}
				return
			}
			label.TextStyle.Bold = false
			entryIndex := cellID.Row - 1
			if entryIndex < len(currentLogEntries) {
				entry := currentLogEntries[entryIndex]
				switch cellID.Col {
				case 0:
					label.SetText(entry.UserName)
				case 1:
					label.SetText(entry.UserID)
				case 2:
					label.SetText(strconv.Itoa(entry.PCID))
				case 3:
					label.SetText(entry.CheckInTime.Format("15:04:05 (Jan 02)"))
				case 4:
					if entry.CheckOutTime.IsZero() {
						label.SetText("-")
					} else {
						label.SetText(entry.CheckOutTime.Format("15:04:05 (Jan 02)"))
					}
				case 5:
					label.SetText(entry.UsageTime)
				}
			}
		})
	logTable.SetColumnWidth(0, 180)
	logTable.SetColumnWidth(1, 100)
	logTable.SetColumnWidth(2, 70)
	logTable.SetColumnWidth(3, 150)
	logTable.SetColumnWidth(4, 150)
	logTable.SetColumnWidth(5, 120)
	return container.NewScroll(logTable)
}

func loadMembers() {
	memberCsvFile, err := os.Open(memberFile)
	if err != nil {
		members = nil
		return
	}
	defer memberCsvFile.Close()

	csvReader := csv.NewReader(memberCsvFile)
	rows, _ := csvReader.ReadAll()

	start := 0
	if len(rows) > 0 && strings.EqualFold(rows[0][1], "email address") {
		start = 1
	}

	members = nil
	for _, row := range rows[start:] {
		if len(row) >= 5 {
			members = append(members, Member{
				Email:         row[1],
				Name:          row[2],
				StudentNumber: row[3],
				PhoneNumber:   row[4],
				ID:            row[3],
			})
		}
	}
}

func getNextMemberID() string {
	return strconv.Itoa(len(members) + 1)
}

func appendMember(memberToAppend Member) {
	memberCsvFile, err := os.OpenFile(memberFile, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		fmt.Println("Error opening member file for append:", err)
		return
	}
	defer memberCsvFile.Close()

	csvReader := csv.NewReader(memberCsvFile)
	existingRows, readErr := csvReader.ReadAll()
	if readErr != nil && readErr != io.EOF {
		fmt.Println("Error reading existing CSV data:", readErr)
		return
	}

	memberCsvFile.Seek(0, 0)
	memberCsvFile.Truncate(0)

	csvWriter := csv.NewWriter(memberCsvFile)

	for _, row := range existingRows {
		if err := csvWriter.Write(row); err != nil {
			fmt.Println("Error writing existing row to CSV:", err)
			return
		}
	}

	newRow := make([]string, 4)
	newRow[0] = ""
	newRow[1] = ""
	newRow[2] = memberToAppend.Name
	newRow[3] = memberToAppend.ID

	if err := csvWriter.Write(newRow); err != nil {
		fmt.Println("Error writing new member to CSV:", err)
		return
	}

	csvWriter.Flush()
	if err := csvWriter.Error(); err != nil {
		fmt.Println("Error flushing CSV writer for member:", err)
	}

	members = append(members, memberToAppend)
}

func memberByID(memberID string) *Member {
	for memberIndex := range members {
		if members[memberIndex].ID == memberID {
			return &members[memberIndex]
		}
	}
	return nil
}

func initData() {
	ensureLogDir()
	allDevices = []Device{}
	for deviceIndex := 1; deviceIndex <= 16; deviceIndex++ {
		allDevices = append(allDevices, Device{ID: deviceIndex, Type: "PC", Status: "free", UserID: ""})
	}
	allDevices = append(allDevices, Device{ID: 17, Type: "Console", Status: "free", UserID: ""})
	allDevices = append(allDevices, Device{ID: 18, Type: "Console", Status: "free", UserID: ""})

	activeUsers = []User{}
	if _, statErr := os.Stat(userDataFile); !os.IsNotExist(statErr) {
		userDataFileHandle, openErr := os.Open(userDataFile)
		if openErr == nil {
			defer userDataFileHandle.Close()
			if json.NewDecoder(userDataFileHandle).Decode(&activeUsers) == nil {
				for userIndex := range activeUsers {
					user := &activeUsers[userIndex]
					for deviceIndex := range allDevices {
						if allDevices[deviceIndex].ID == user.PCID {
							allDevices[deviceIndex].Status = "occupied"
							if allDevices[deviceIndex].Type == "PC" {
								allDevices[deviceIndex].UserID = user.ID
							}
							break
						}
					}
				}
			} else {
				activeUsers = []User{}
			}
		}
	}
	loadMembers()
	if activeUserNetworkInstance != nil {
		activeUserNetworkInstance.pcPositions = make(map[string]fyne.Position)
		activeUserNetworkInstance.UpdateUsers(activeUsers)
	}
}

func saveData() {
	ensureLogDir()
	userDataFileHandle, err := os.Create(userDataFile)
	if err != nil {
		fmt.Println("Error creating user data file for save:", err)
		return
	}
	defer userDataFileHandle.Close()
	if err := json.NewEncoder(userDataFileHandle).Encode(activeUsers); err != nil {
		fmt.Println("Error encoding user data to file:", err)
	}
}

func getUserByID(userID string) *User {
	for userIndex := range activeUsers {
		if activeUsers[userIndex].ID == userID {
			return &activeUsers[userIndex]
		}
	}
	return nil
}

func getDeviceByID(deviceID int) *Device {
	for deviceIndex := range allDevices {
		if allDevices[deviceIndex].ID == deviceID {
			return &allDevices[deviceIndex]
		}
	}
	return nil
}

func getDeviceByUserID(userID string) *Device {
	for deviceIndex := range allDevices {
		if allDevices[deviceIndex].UserID == userID {
			return &allDevices[deviceIndex]
		}
	}
	return nil
}

// returns the active user IDs currently assigned to a device ID
func activeUserIDsOnDevice(deviceID int) []string {
	ids := []string{}
	for _, u := range activeUsers {
		if u.PCID == deviceID {
			ids = append(ids, u.ID)
		}
	}
	return ids
}

func registerUser(name, userID string, deviceID int) error {
	if getUserByID(userID) != nil {
		existingUser := getUserByID(userID)
		return fmt.Errorf("user ID %s (%s) already checked in on Device %d", userID, existingUser.Name, existingUser.PCID)
	}
	device := getDeviceByID(deviceID)
	if device == nil {
		return fmt.Errorf("device ID %d does not exist", deviceID)
	}

	if device.Type == "PC" {
		if device.Status != "free" {
			return fmt.Errorf("device %d is busy (occupied by UserID: %s)", deviceID, device.UserID)
		}
		device.Status = "occupied"
		device.UserID = userID
	} else {
		device.Status = "occupied"
	}

	newUser := User{ID: userID, Name: name, CheckInTime: time.Now(), PCID: deviceID}
	activeUsers = append(activeUsers, newUser)

	if memberByID(userID) == nil {
		appendMember(Member{Name: name, ID: userID})
	}
	saveData()
	go recordLogEvent(true, newUser, deviceID, nil)
	refreshTrigger <- true
	return nil
}

func checkoutUser(userID string) error {
	userToCheckout := getUserByID(userID)
	if userToCheckout == nil {
		return fmt.Errorf("user ID %s not found among active users", userID)
	}

	userIndex := -1
	for index, user := range activeUsers {
		if user.ID == userID {
			userIndex = index
			break
		}
	}
	if userIndex == -1 {
		return fmt.Errorf("user %s consistency error: found by ID but not in slice iteration", userID)
	}

	originalCheckInTime := userToCheckout.CheckInTime
	loggedDeviceID := userToCheckout.PCID
	deviceUsed := getDeviceByID(loggedDeviceID)

	activeUsers = append(activeUsers[:userIndex], activeUsers[userIndex+1:]...)

	if deviceUsed != nil {
		if deviceUsed.Type == "PC" {
			deviceUsed.Status = "free"
			deviceUsed.UserID = ""
		} else {
			remaining := activeUserIDsOnDevice(deviceUsed.ID)
			if len(remaining) == 0 {
				deviceUsed.Status = "free"
			} else {
				deviceUsed.Status = "occupied"
			}
		}
	}

	if activeUserNetworkInstance != nil {
		delete(activeUserNetworkInstance.pcPositions, userID)
	}

	saveData()
	go recordLogEvent(false, *userToCheckout, loggedDeviceID, &originalCheckInTime)
	refreshTrigger <- true
	return nil
}

func getFormattedUsageDuration(startTime time.Time) string {
	return formatDuration(time.Since(startTime))
}

type DeviceButton struct {
	widget.BaseWidget
	device *Device
}

func NewDeviceButton(device *Device) *DeviceButton {
	button := &DeviceButton{device: device}
	button.ExtendBaseWidget(button)
	return button
}

func (button *DeviceButton) Tapped(_ *fyne.PointEvent) {
	if button.device.Status == "occupied" {
		if button.device.Type == "Console" {
			userIDs := activeUserIDsOnDevice(button.device.ID)
			if len(userIDs) == 0 {
				return
			}
			display := []string{}
			for _, id := range userIDs {
				u := getUserByID(id)
				name := "Unknown User"
				if u != nil {
					name = u.Name
				}
				display = append(display, fmt.Sprintf("%s (ID: %s)", name, id))
			}
			selector := widget.NewSelectEntry(display)
			formItems := []*widget.FormItem{{Text: "User on " + button.device.Type, Widget: selector}}
			dialogInstance := dialog.NewForm("Checkout From "+button.device.Type, "Check Out", "Cancel", formItems, func(confirmed bool) {
				if !confirmed {
					return
				}
				choice := strings.TrimSpace(selector.Text)
				if choice == "" {
					return
				}
				var targetID string
				for i, s := range display {
					if s == choice {
						targetID = userIDs[i]
						break
					}
				}
				if targetID == "" {
					return
				}
				if err := checkoutUser(targetID); err != nil {
					dialog.ShowError(err, mainWindow)
				}
			}, mainWindow)
			dialogInstance.Resize(fyne.NewSize(420, dialogInstance.MinSize().Height))
			dialogInstance.Show()
			return
		}

		user := getUserByID(button.device.UserID)
		userName := "Unknown User"
		if user != nil {
			userName = user.Name
		}
		dialog.ShowConfirm("Confirm Checkout", fmt.Sprintf("Checkout %s from %s %d?", userName, button.device.Type, button.device.ID),
			func(confirmed bool) {
				if confirmed {
					if err := checkoutUser(button.device.UserID); err != nil {
						dialog.ShowError(err, mainWindow)
					}
				}
			}, mainWindow)
	} else {
		showCheckInDialogShared(button.device.ID, true)
	}
}

func (button *DeviceButton) MouseIn(_ *desktop.MouseEvent) {
	if deviceHoverDetailLabel == nil {
		return
	}
	if button.device.Status != "occupied" {
		deviceHoverDetailLabel.SetText("")
		return
	}
	if button.device.Type == "Console" {
		userIDs := activeUserIDsOnDevice(button.device.ID)
		if len(userIDs) == 0 {
			deviceHoverDetailLabel.SetText("")
			return
		}
		lines := []string{}
		for _, id := range userIDs {
			u := getUserByID(id)
			if u != nil {
				lines = append(lines, fmt.Sprintf("%s (ID: %s) In: %s  |  Usage: %s",
					u.Name, u.ID, u.CheckInTime.Format("15:04:05 (Jan 02)"), getFormattedUsageDuration(u.CheckInTime)))
			}
		}
		deviceHoverDetailLabel.SetText(fmt.Sprintf("%s %d:\n%s", button.device.Type, button.device.ID, strings.Join(lines, "\n")))
		return
	}
	user := getUserByID(button.device.UserID)
	if user != nil {
		device := getDeviceByID(user.PCID)
		deviceType := "Unknown Device"
		if device != nil {
			deviceType = device.Type
		}
		infoText := fmt.Sprintf("Using %s %d: %s (ID: %s)\nChecked In: %s  |  Usage: %s",
			deviceType, user.PCID, user.Name, user.ID,
			user.CheckInTime.Format("15:04:05 (Jan 02)"), getFormattedUsageDuration(user.CheckInTime))
		deviceHoverDetailLabel.SetText(infoText)
	} else {
		deviceHoverDetailLabel.SetText(fmt.Sprintf("%s %d: User details not found (UserID: %s).", button.device.Type, button.device.ID, button.device.UserID))
	}
}

func (button *DeviceButton) MouseOut() {
	if deviceHoverDetailLabel != nil {
		deviceHoverDetailLabel.SetText("")
	}
}
func (button *DeviceButton) Dragged(event *fyne.DragEvent) {}
func (button *DeviceButton) DragEnd()                      {}
func (button *DeviceButton) CreateRenderer() fyne.WidgetRenderer {
	var imagePath string
	baseImageName := "free.png"
	if button.device.Type == "Console" {
		baseImageName = "console.png"
	}
	imagePath = filepath.Join(imgBaseDir, baseImageName)
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		fmt.Printf("Warning: Image %s not found\n", imagePath)
	}
	image := canvas.NewImageFromFile(imagePath)
	image.FillMode = canvas.ImageFillContain
	image.SetMinSize(fyne.NewSize(64, 64))

	labelText := strconv.Itoa(button.device.ID)
	if button.device.ID == 17 {
		labelText = "Xbox " + labelText
	}
	if button.device.ID == 18 {
		labelText = "PS4 " + labelText
	}
	label := widget.NewLabel(labelText)
	label.TextStyle = fyne.TextStyle{Bold: true}
	label.Alignment = fyne.TextAlignCenter

	content := container.NewVBox(image, label)
	renderer := &deviceRenderer{button: button, image: image, label: label, objects: []fyne.CanvasObject{content}}
	renderer.Refresh()
	return renderer
}

type deviceRenderer struct {
	button  *DeviceButton
	image   *canvas.Image
	label   *widget.Label
	objects []fyne.CanvasObject
}

func (renderer *deviceRenderer) Layout(size fyne.Size)        { renderer.objects[0].Resize(size) }
func (renderer *deviceRenderer) MinSize() fyne.Size           { return renderer.objects[0].MinSize() }
func (renderer *deviceRenderer) Objects() []fyne.CanvasObject { return renderer.objects }
func (renderer *deviceRenderer) Destroy()                     {}
func (renderer *deviceRenderer) BackgroundColor() color.Color { return color.Transparent }
func (renderer *deviceRenderer) Refresh() {
	var imagePath string
	var baseImageName string
	if renderer.button.device.Status == "free" {
		if renderer.button.device.Type == "PC" {
			baseImageName = "free.png"
		} else {
			baseImageName = "console.png"
		}
	} else {
		if renderer.button.device.Type == "PC" {
			baseImageName = "busy.png"
		} else {
			baseImageName = "console_busy.png"
		}
	}
	imagePath = filepath.Join(imgBaseDir, baseImageName)
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		fmt.Printf("Warning: Image %s not found during refresh\n", imagePath)
	}
	renderer.image.File = imagePath
	renderer.image.Refresh()

	labelText := strconv.Itoa(renderer.button.device.ID)
	if renderer.button.device.ID == 17 {
		labelText = "Xbox " + labelText
	}
	if renderer.button.device.ID == 18 {
		labelText = "PS4 " + labelText
	}
	renderer.label.SetText(labelText)
	renderer.label.Refresh()
	canvas.Refresh(renderer.button)
}

func buildDeviceRoomContent() fyne.CanvasObject {
	layoutWidget := NewDeviceStatusLayoutWidget()
	layoutWidget.UpdateDevices()

	if deviceHoverDetailLabel == nil {
		deviceHoverDetailLabel = widget.NewLabel("")
		deviceHoverDetailLabel.Wrapping = fyne.TextWrapWord
		deviceHoverDetailLabel.Alignment = fyne.TextAlignCenter
	}

	// right side = the same network widget used previously in "Active Users" tab
	if activeUserNetworkInstance == nil {
		activeUserNetworkInstance = NewActiveUserNetworkWidget()
	}
	activeUserNetworkInstance.UpdateUsers(activeUsers)

	right := container.NewBorder(
		widget.NewLabelWithStyle("Active Users", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		nil, nil, nil,
		activeUserNetworkInstance,
	)

	split := container.NewHSplit(layoutWidget, right)
	split.Offset = 0.68 // left/right ratio; tweak to taste

	return container.NewBorder(nil, deviceHoverDetailLabel, nil, nil, split)
}

func showCheckInDialogShared(deviceID int, deviceIDIsFixed bool) {
	const (
		dialogWidth             float32 = 460
		dialogBaseHeight        float32 = 260
		dialogResultsListHeight float32 = 110
	)

	searchEntry := widget.NewEntry()
	searchEntry.SetPlaceHolder("Search Existing Member (Name/ID)...")
	nameEntry := widget.NewEntry()
	idEntry := widget.NewEntry()
	deviceEntryWidget := widget.NewEntry()

	nameEntry.SetPlaceHolder("Full Name")
	idEntry.SetPlaceHolder("ID")

	noIDButton := widget.NewButton("No ID?", func() {
		idEntry.SetText("LOUNGE-" + getNextMemberID())
	})
	noIDButton.Resize(fyne.NewSize(55, 25))

	if deviceIDIsFixed {
		deviceEntryWidget.SetText(strconv.Itoa(deviceID))
		deviceEntryWidget.Disable()
	} else {
		deviceEntryWidget.SetPlaceHolder("Enter Device ID")
	}

	var filteredMembersForDialog []Member
	var resultsListDialog *widget.List
	var dialogReference dialog.Dialog

	resultsListDialog = widget.NewList(
		func() int { return len(filteredMembersForDialog) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(listItemIndex widget.ListItemID, itemCanvasObject fyne.CanvasObject) {
			if listItemIndex >= 0 && listItemIndex < len(filteredMembersForDialog) {
				itemCanvasObject.(*widget.Label).SetText(fmt.Sprintf("%s (%s)", filteredMembersForDialog[listItemIndex].Name, filteredMembersForDialog[listItemIndex].ID))
			}
		})

	scrollableResultsDialog := container.NewScroll(resultsListDialog)
	scrollableResultsDialog.SetMinSize(fyne.NewSize(dialogWidth-40, dialogResultsListHeight-10))
	scrollableResultsDialog.Hide()

	resultsListDialog.OnSelected = func(selectedListItemID widget.ListItemID) {
		if selectedListItemID >= 0 && selectedListItemID < len(filteredMembersForDialog) {
			selectedMember := filteredMembersForDialog[selectedListItemID]
			nameEntry.SetText(selectedMember.Name)
			idEntry.SetText(selectedMember.ID)
			searchEntry.SetText("")
			scrollableResultsDialog.Hide()
			resultsListDialog.UnselectAll()
			filteredMembersForDialog = []Member{}
			resultsListDialog.Refresh()
			if dialogReference != nil {
				dialogReference.Resize(fyne.NewSize(dialogWidth, dialogBaseHeight))
			}
		}
	}

	searchEntry.OnChanged = func(searchTextValue string) {
		searchTextValue = strings.ToLower(strings.TrimSpace(searchTextValue))
		if searchTextValue == "" {
			filteredMembersForDialog = []Member{}
		} else {
			newFiltered := []Member{}
			for _, member := range members {
				if strings.Contains(strings.ToLower(member.Name), searchTextValue) || strings.Contains(strings.ToLower(member.ID), searchTextValue) {
					newFiltered = append(newFiltered, member)
				}
			}
			filteredMembersForDialog = newFiltered
		}
		resultsListDialog.Refresh()

		if dialogReference != nil {
			if len(filteredMembersForDialog) > 0 {
				scrollableResultsDialog.Show()
				dialogReference.Resize(fyne.NewSize(dialogWidth, dialogBaseHeight+dialogResultsListHeight))
			} else {
				scrollableResultsDialog.Hide()
				dialogReference.Resize(fyne.NewSize(dialogWidth, dialogBaseHeight))
			}
		}
	}

	userIDRow := container.NewBorder(nil, nil, nil, noIDButton, idEntry)

	formWidget := widget.NewForm(
		widget.NewFormItem("Name:", nameEntry),
		widget.NewFormItem("User ID:", userIDRow),
		widget.NewFormItem("Device ID:", deviceEntryWidget),
	)

	onConfirmAction := func() {
		userIDString := strings.TrimSpace(idEntry.Text)
		userName := strings.TrimSpace(nameEntry.Text)

		if userName == "" || userIDString == "" {
			dialog.ShowError(fmt.Errorf("name and ID are required"), mainWindow)
			return
		}

		targetDeviceID := 0
		var parseErr error
		if deviceIDIsFixed {
			targetDeviceID = deviceID
		} else {
			deviceIDText := strings.TrimSpace(deviceEntryWidget.Text)
			if deviceIDText == "" {
				dialog.ShowError(fmt.Errorf("device ID is required"), mainWindow)
				return
			}
			targetDeviceID, parseErr = strconv.Atoi(deviceIDText)
			if parseErr != nil {
				dialog.ShowError(fmt.Errorf("invalid Device ID: must be a number"), mainWindow)
				return
			}
		}

		err := registerUser(userName, userIDString, targetDeviceID)
		if err != nil {
			dialog.ShowError(err, mainWindow)
			return
		}
		if dialogReference != nil {
			dialogReference.Hide()
		}
	}

	dialogContent := container.NewVBox(searchEntry, scrollableResultsDialog, formWidget)

	dialogReference = dialog.NewCustomConfirm("Check In User", "Check In", "Cancel", dialogContent, func(confirmed bool) {
		if confirmed {
			onConfirmAction()
		}
	}, mainWindow)
	dialogReference.Resize(fyne.NewSize(dialogWidth, dialogBaseHeight))
	dialogReference.Show()
}

func showCheckInDialog() { showCheckInDialogShared(0, false) }

func showCheckOutDialog() {
	if len(activeUsers) == 0 {
		dialog.ShowInformation("Check Out", "No active users to check out.", mainWindow)
		return
	}

	activeUserDisplayStrings := make([]string, len(activeUsers))
	activeUserIDs := make([]string, len(activeUsers))

	for index, user := range activeUsers {
		maxNameLength := 25
		displayName := user.Name
		if len(displayName) > maxNameLength {
			displayName = displayName[:maxNameLength-3] + "..."
		}
		activeUserDisplayStrings[index] = fmt.Sprintf("%s (ID: %s, PC: %d)", displayName, user.ID, user.PCID)
		activeUserIDs[index] = user.ID
	}

	idSelector := widget.NewSelectEntry(activeUserDisplayStrings)
	idSelector.SetPlaceHolder("Select User to Check Out")

	formItems := []*widget.FormItem{
		{Text: "User:", Widget: idSelector},
	}

	dialogInstance := dialog.NewForm("Check Out User", "Check Out", "Cancel", formItems, func(confirmed bool) {
		if !confirmed {
			return
		}
		selectedText := strings.TrimSpace(idSelector.Text)
		if selectedText == "" {
			dialog.ShowError(fmt.Errorf("no user selected"), mainWindow)
			return
		}

		var userIDToCheckout string
		foundSelection := false
		for index, displayString := range activeUserDisplayStrings {
			if displayString == selectedText {
				userIDToCheckout = activeUserIDs[index]
				foundSelection = true
				break
			}
		}

		if !foundSelection {
			dialog.ShowError(fmt.Errorf("invalid user selection"), mainWindow)
			return
		}

		if err := checkoutUser(userIDToCheckout); err != nil {
			dialog.ShowError(err, mainWindow)
		}
	}, mainWindow)

	dialogInstance.Resize(fyne.NewSize(450, dialogInstance.MinSize().Height))
	dialogInstance.Show()
}

func main() {
	initData()
	if err := os.MkdirAll(imgBaseDir, 0o755); err != nil {
		fmt.Printf("Warning: Unable to create image directory '%s': %v. Images might not load.\n", imgBaseDir, err)
	}

	loungeApp := app.New()
	loungeApp.Settings().SetTheme(newClassicTheme())
	mainWindow = loungeApp.NewWindow("Lounge Management System")
	mainWindow.Resize(fyne.NewSize(1080, 720))

	deviceHoverDetailLabel = widget.NewLabel("")
	deviceHoverDetailLabel.Wrapping = fyne.TextWrapWord
	deviceHoverDetailLabel.Alignment = fyne.TextAlignCenter

	activeUserNetworkInstance = NewActiveUserNetworkWidget()

	deviceStatusTabContent := buildDeviceRoomContent()
	logView := buildLogView()

	resetLayoutButton := widget.NewButton("Reset Network Layout", func() {
		if activeUserNetworkInstance != nil {
			activeUserNetworkInstance.pcPositions = make(map[string]fyne.Position)
			activeUserNetworkInstance.UpdateUsers(activeUsers)
		}
	})
	resetLayoutButton.Hide()

	checkInButton := widget.NewButtonWithIcon("Check In", theme.ContentAddIcon(), showCheckInDialog)
	checkOutButton := widget.NewButtonWithIcon("Check Out", theme.ContentRemoveIcon(), showCheckOutDialog)
	toolbar := container.NewHBox(checkInButton, checkOutButton, layout.NewSpacer(), resetLayoutButton)

	totalDevicesLabel := widget.NewLabel("")
	activeUsersLabel := widget.NewLabel("")

	updateStatusLabels := func() {
		totalDevicesLabel.SetText(fmt.Sprintf("Total Devices: %d", len(allDevices)))
		activeUsersLabel.SetText(fmt.Sprintf("Active Users: %d", len(activeUsers)))
	}
	updateStatusLabels()

	statusBar := container.NewHBox(totalDevicesLabel, widget.NewLabel(" | "), activeUsersLabel)

	tabs = container.NewAppTabs(
		container.NewTabItem("Device Status", deviceStatusTabContent),
		container.NewTabItem("Log", logView),
	)

	for index, tabItem := range tabs.Items {
		switch tabItem.Text {
		case "Device Status":
			deviceStatusTabIndex = index
		case "Log":
			logTabIndex = index
		}
	}

	tabs.SetTabLocation(container.TabLocationTop)
	tabs.OnSelected = func(selectedTabItem *container.TabItem) {
		resetLayoutButton.Hide()

		if selectedTabItem.Text == "Log" && logTabIndex != -1 {
			updateCurrentLogEntriesCache()
			if logTable != nil {
				logTable.Refresh()
			}
			logRefreshPending = false
		}
	}

	topBarLayout := container.NewVBox(toolbar, widget.NewSeparator())
	bottomBarLayout := container.NewVBox(widget.NewSeparator(), statusBar)
	mainApplicationContent := container.NewBorder(topBarLayout, bottomBarLayout, nil, nil, tabs)

	mainWindow.SetContent(mainApplicationContent)

	go func() {
		uiUpdateTicker := time.NewTicker(time.Second)
		dailyLogCheckTicker := time.NewTicker(time.Minute * 5)
		lastLogFileDate := time.Now().Format("2006-01-02")
		defer uiUpdateTicker.Stop()
		defer dailyLogCheckTicker.Stop()

		for {
			select {
			case <-uiUpdateTicker.C:
				fyne.Do(func() {
					if tabs.Selected() != nil && tabs.Selected().Text == "Device Status" {
						if activeUserNetworkInstance != nil {
							activeUserNetworkInstance.Refresh()
						}
					}
				})
			case <-dailyLogCheckTicker.C:
				fyne.Do(func() {
					currentDate := time.Now().Format("2006-01-02")
					if currentDate != lastLogFileDate {
						lastLogFileDate = currentDate
						fmt.Println("Date changed, new log file will be used:", getLogFilePath())
						if tabs.Selected() != nil && tabs.Selected().Text == "Log" && logTable != nil {
							updateCurrentLogEntriesCache()
							logTable.Refresh()
						} else {
							logRefreshPending = true
						}
					}
				})
			case <-refreshTrigger:
				fyne.Do(func() {
					if deviceStatusTabIndex != -1 && tabs != nil && len(tabs.Items) > deviceStatusTabIndex {
						tabs.Items[deviceStatusTabIndex].Content = buildDeviceRoomContent()
					}

					if activeUserNetworkInstance != nil {
						activeUserNetworkInstance.UpdateUsers(activeUsers)
					}

					updateStatusLabels()

					if logRefreshPending || (tabs.Selected() != nil && tabs.Selected().Text == "Log") {
						updateCurrentLogEntriesCache()
						if logTable != nil {
							logTable.Refresh()
						}
						logRefreshPending = false
					}

					if tabs != nil {
						tabs.Refresh()
					}
					if mainWindow != nil && mainWindow.Content() != nil {
						mainWindow.Content().Refresh()
					}
				})
			}
		}
	}()

	mainWindow.SetMaster()
	mainWindow.ShowAndRun()
}

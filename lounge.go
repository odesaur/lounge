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
	allDevices        []Device
	activeUsers       []User
	members           []Member
	mainWindow        fyne.Window
	logTable          *widget.Table
	refreshTrigger    = make(chan bool, 1)
	logRefreshPending = false
	logFileMutex      sync.Mutex
	currentLogEntries []LogEntry

	assignmentUserID         string
	assignmentNoticeLabel    *widget.Label
	checkInInlineForm        *fyne.Container
	checkInNameEntry         *widget.Entry
	checkInIDEntry           *widget.Entry
	checkInSearchEntry       *widget.Entry
	checkInResultsList       *widget.List
	filteredMembersForInline []Member
	pendingIconsBox          *fyne.Container
	raccoonIconResource      fyne.Resource
)

type PendingUserIcon struct {
	widget.BaseWidget
	user     User
	resource fyne.Resource
	onAssign func(User)
}

func newPendingUserIcon(u User, res fyne.Resource, onAssign func(User)) *PendingUserIcon {
	w := &PendingUserIcon{user: u, resource: res, onAssign: onAssign}
	w.ExtendBaseWidget(w)
	return w
}

type pendingUserIconRenderer struct {
	widget  *PendingUserIcon
	image   *canvas.Image
	objects []fyne.CanvasObject
}

func (w *PendingUserIcon) CreateRenderer() fyne.WidgetRenderer {
	img := canvas.NewImageFromResource(w.resource)
	img.FillMode = canvas.ImageFillContain
	const s float32 = 32
	img.SetMinSize(fyne.NewSize(s, s))
	img.Resize(fyne.NewSize(s, s))
	center := container.NewCenter(img)
	return &pendingUserIconRenderer{widget: w, image: img, objects: []fyne.CanvasObject{center}}
}

func (r *pendingUserIconRenderer) Layout(size fyne.Size)        { r.objects[0].Resize(size) }
func (r *pendingUserIconRenderer) MinSize() fyne.Size           { return fyne.NewSize(36, 36) }
func (r *pendingUserIconRenderer) Refresh()                     { r.image.Resource = r.widget.resource; r.image.Refresh() }
func (r *pendingUserIconRenderer) Objects() []fyne.CanvasObject { return r.objects }
func (r *pendingUserIconRenderer) Destroy()                     {}

func (w *PendingUserIcon) Tapped(_ *fyne.PointEvent) {
	d := dialog.NewCustomConfirm(
		fmt.Sprintf("Queued: %s (%s)", w.user.Name, w.user.ID),
		"Assign",
		"Remove",
		widget.NewLabel("Choose an action for this queued user."),
		func(assign bool) {
			if assign {
				if w.onAssign != nil {
					w.onAssign(w.user)
				}
			} else {
				if err := removeQueuedUser(w.user.ID); err != nil {
					dialog.ShowError(err, mainWindow)
				}
			}
		},
		mainWindow,
	)
	d.Show()
}

func ensureRaccoonIcon() fyne.Resource {
	if raccoonIconResource != nil {
		return raccoonIconResource
	}
	path := filepath.Join(imgBaseDir, "raccoon.png")
	if _, err := os.Stat(path); err == nil {
		res, _ := fyne.LoadResourceFromPath(path)
		raccoonIconResource = res
	} else {
		raccoonIconResource = theme.ComputerIcon()
	}
	return raccoonIconResource
}

func refreshPendingIcons() {
	if pendingIconsBox == nil {
		return
	}
	pendingIconsBox.Objects = pendingIconsBox.Objects[:0]
	iconRes := ensureRaccoonIcon()
	for _, u := range getPendingUsers() {
		user := u
		icon := newPendingUserIcon(user, iconRes, func(sel User) {
			assignmentUserID = sel.ID
			if assignmentNoticeLabel != nil {
				assignmentNoticeLabel.SetText(fmt.Sprintf("Assignment mode: click a free device for %s (%s).", sel.Name, sel.ID))
			}
		})
		pendingIconsBox.Add(icon)
	}
	pendingIconsBox.Refresh()
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

	pcIconSize      float32
	consoleIconSize float32
	slotMargin      float32
}

func NewDeviceStatusLayoutWidget() *DeviceStatusLayoutWidget {
	layoutWidget := &DeviceStatusLayoutWidget{
		deviceToSlot:    make(map[int]int),
		pcIconSize:      64,
		consoleIconSize: 64,
		slotMargin:      24,
	}
	layoutWidget.loadDeviceLayout()
	layoutWidget.ExtendBaseWidget(layoutWidget)
	return layoutWidget
}

func (layoutWidget *DeviceStatusLayoutWidget) defaultOrder() []int {
	return []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18}
}

func (layoutWidget *DeviceStatusLayoutWidget) ensureMapping() {
	if len(layoutWidget.deviceToSlot) == len(allDevices) {
		return
	}
	seen := make(map[int]bool)
	for deviceID := range layoutWidget.deviceToSlot {
		seen[deviceID] = true
	}
	order := layoutWidget.defaultOrder()
	slotIndex := 0
	occupiedSlots := make(map[int]bool)
	for _, slot := range layoutWidget.deviceToSlot {
		occupiedSlots[slot] = true
	}
	for _, deviceID := range order {
		if seen[deviceID] {
			continue
		}
		for {
			if !occupiedSlots[slotIndex] {
				layoutWidget.deviceToSlot[deviceID] = slotIndex
				occupiedSlots[slotIndex] = true
				slotIndex++
				break
			}
			slotIndex++
		}
	}
}

func (layoutWidget *DeviceStatusLayoutWidget) loadDeviceLayout() {
	_ = ensureLogDir()
	data, err := os.ReadFile(deviceLayoutFile)
	if err != nil || len(data) == 0 {
		layoutWidget.deviceToSlot = make(map[int]int)
		layoutWidget.ensureMapping()
		layoutWidget.saveDeviceLayout()
		return
	}
	type layoutEntry struct{ DeviceID, Slot int }
	var entries []layoutEntry
	if json.Unmarshal(data, &entries) != nil {
		layoutWidget.deviceToSlot = make(map[int]int)
		layoutWidget.ensureMapping()
		layoutWidget.saveDeviceLayout()
		return
	}
	layoutWidget.deviceToSlot = make(map[int]int)
	for _, entry := range entries {
		layoutWidget.deviceToSlot[entry.DeviceID] = entry.Slot
	}
	layoutWidget.ensureMapping()
}

func (layoutWidget *DeviceStatusLayoutWidget) saveDeviceLayout() {
	type layoutEntry struct{ DeviceID, Slot int }
	entries := make([]layoutEntry, 0, len(layoutWidget.deviceToSlot))
	for deviceID, slot := range layoutWidget.deviceToSlot {
		entries = append(entries, layoutEntry{DeviceID: deviceID, Slot: slot})
	}
	data, _ := json.MarshalIndent(entries, "", "  ")
	_ = os.WriteFile(deviceLayoutFile, data, 0o644)
}

func (layoutWidget *DeviceStatusLayoutWidget) computeSlots() {
	if layoutWidget.containerSize.IsZero() {
		return
	}
	layoutWidget.slotPositions = layoutWidget.slotPositions[:0]
	total := len(allDevices)
	if total == 0 {
		return
	}

	pcCount := 0
	consoleCount := 0
	for _, device := range allDevices {
		if device.Type == "PC" {
			pcCount++
		} else {
			consoleCount++
		}
	}

	availableWidth := layoutWidget.containerSize.Width - layoutWidget.slotMargin*2
	if availableWidth <= 0 {
		availableWidth = layoutWidget.containerSize.Width
	}
	if availableWidth <= 0 {
		availableWidth = 400
	}
	consoleAreaWidth := clampFloat(layoutWidget.containerSize.Width*0.18, 130, 220)
	if pcCount == 0 {
		consoleAreaWidth = 0
	}
	pcAreaWidth := availableWidth - consoleAreaWidth - layoutWidget.slotMargin
	maxColumns := 4
	if pcAreaWidth < float32(maxColumns)*70 {
		consoleAreaWidth = clampFloat(layoutWidget.containerSize.Width*0.12, 100, 140)
		pcAreaWidth = availableWidth - consoleAreaWidth
	}
	if pcAreaWidth < float32(maxColumns)*60 {
		consoleAreaWidth = 0
		pcAreaWidth = availableWidth
	}
	if pcAreaWidth <= 0 {
		pcAreaWidth = availableWidth
	}

	columnWidth := pcAreaWidth / float32(maxColumns)
	if columnWidth <= 0 {
		columnWidth = 80
	}

	rowCounts := []int{3, 3, 3, 3, 4}
	totalRows := len(rowCounts)
	availableHeight := layoutWidget.containerSize.Height - layoutWidget.slotMargin*2
	if availableHeight <= 0 {
		availableHeight = layoutWidget.containerSize.Height
	}
	if availableHeight <= 0 {
		availableHeight = float32(totalRows) * 120
	}
	rowHeight := availableHeight / float32(totalRows)
	if rowHeight <= 0 {
		rowHeight = 120
	}

	iconBase := float32(math.Min(float64(columnWidth), float64(rowHeight))) * 0.55
	layoutWidget.pcIconSize = clampFloat(iconBase, 40, 96)
	layoutWidget.consoleIconSize = clampFloat(iconBase*1.05, 48, 104)

	gridOriginX := layoutWidget.slotMargin + (pcAreaWidth-columnWidth*float32(maxColumns))/2
	if gridOriginX < layoutWidget.slotMargin {
		gridOriginX = layoutWidget.slotMargin
	}

	added := 0
	for rowIndex, cols := range rowCounts {
		if added >= pcCount {
			break
		}
		centerY := layoutWidget.slotMargin + rowHeight*float32(rowIndex) + rowHeight/2
		rowStart := gridOriginX + (float32(maxColumns-cols)*columnWidth)/2
		for col := 0; col < cols && added < pcCount; col++ {
			centerX := rowStart + columnWidth*float32(col) + columnWidth/2
			layoutWidget.slotPositions = append(layoutWidget.slotPositions, fyne.NewPos(centerX, centerY))
			added++
		}
	}

	consoleX := layoutWidget.containerSize.Width - layoutWidget.slotMargin - consoleAreaWidth/2
	if consoleAreaWidth == 0 {
		consoleX = layoutWidget.containerSize.Width - layoutWidget.slotMargin - columnWidth/2
	}
	consoleSpacing := layoutWidget.consoleIconSize + 40
	consoleY := layoutWidget.slotMargin + layoutWidget.consoleIconSize
	for i := 0; i < consoleCount; i++ {
		layoutWidget.slotPositions = append(layoutWidget.slotPositions, fyne.NewPos(consoleX, consoleY+float32(i)*consoleSpacing))
	}

	for len(layoutWidget.slotPositions) < total {
		layoutWidget.slotPositions = append(layoutWidget.slotPositions, fyne.NewPos(layoutWidget.slotMargin+layoutWidget.pcIconSize, layoutWidget.slotMargin+layoutWidget.pcIconSize))
	}
	if len(layoutWidget.slotPositions) > total {
		layoutWidget.slotPositions = layoutWidget.slotPositions[:total]
	}
}

func clampFloat(value, minValue, maxValue float32) float32 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func (layoutWidget *DeviceStatusLayoutWidget) UpdateDevices() {
	layoutWidget.ensureMapping()
	layoutWidget.Refresh()
}

func (layoutWidget *DeviceStatusLayoutWidget) Tapped(tapEvent *fyne.PointEvent) {
	for _, device := range allDevices {
		center := layoutWidget.positionForDevice(device.ID)
		size := layoutWidget.iconSizeForDevice(device.ID)
		topLeft := fyne.NewPos(center.X-size/2, center.Y-size/2)

		if tapEvent.Position.X < topLeft.X || tapEvent.Position.X > topLeft.X+size ||
			tapEvent.Position.Y < topLeft.Y || tapEvent.Position.Y > topLeft.Y+size {
			continue
		}

		if assignmentUserID != "" {
			targetUserID := assignmentUserID
			assignmentUserID = ""
			if assignmentNoticeLabel != nil {
				assignmentNoticeLabel.SetText("")
			}
			if err := assignQueuedUserToDevice(targetUserID, device.ID); err != nil {
				dialog.ShowError(err, mainWindow)
			}
			return
		}

		if device.Type == "Console" {
			if device.Status == "occupied" {
				showConsoleCheckoutDialog(device)
			} else {
				showCheckInDialogShared(device.ID, true)
			}
			return
		}

		if device.Status == "occupied" {
			user := getUserByID(device.UserID)
			userName := "Unknown User"
			if user != nil {
				userName = user.Name
			}
			dialog.ShowConfirm(
				"Confirm Checkout",
				fmt.Sprintf("Checkout %s from PC %d?", userName, device.ID),
				func(confirm bool) {
					if confirm {
						if err := checkoutUser(device.UserID); err != nil {
							dialog.ShowError(err, mainWindow)
						}
					}
				},
				mainWindow,
			)
			return
		}

		showCheckInDialogShared(device.ID, true)
		return
	}
}

func (layoutWidget *DeviceStatusLayoutWidget) MouseDown(mouseEvent *desktop.MouseEvent) {
	if mouseEvent.Button != desktop.MouseButtonSecondary {
		return
	}
	for _, device := range allDevices {
		if device.Type != "Console" {
			continue
		}
		center := layoutWidget.positionForDevice(device.ID)
		size := layoutWidget.iconSizeForDevice(device.ID)
		topLeft := fyne.NewPos(center.X-size/2, center.Y-size/2)
		if mouseEvent.Position.X >= topLeft.X && mouseEvent.Position.X <= topLeft.X+size &&
			mouseEvent.Position.Y >= topLeft.Y && mouseEvent.Position.Y <= topLeft.Y+size {
			if device.Status == "occupied" {
				showConsoleCheckoutDialog(device)
			}
			return
		}
	}
}

func (layoutWidget *DeviceStatusLayoutWidget) MouseUp(_ *desktop.MouseEvent) {}

func (layoutWidget *DeviceStatusLayoutWidget) Dragged(dragEvent *fyne.DragEvent) {
	if !layoutWidget.isDragging {
		for _, device := range allDevices {
			center := layoutWidget.positionForDevice(device.ID)
			size := layoutWidget.iconSizeForDevice(device.ID)
			topLeft := fyne.NewPos(center.X-size/2, center.Y-size/2)
			if dragEvent.Position.X >= topLeft.X && dragEvent.Position.X <= topLeft.X+size &&
				dragEvent.Position.Y >= topLeft.Y && dragEvent.Position.Y <= topLeft.Y+size {
				layoutWidget.isDragging = true
				layoutWidget.draggingDeviceID = device.ID
				layoutWidget.dragOffset = fyne.NewPos(dragEvent.Position.X-center.X, dragEvent.Position.Y-center.Y)
				layoutWidget.transientDragPos = center
				break
			}
		}
	}
	if layoutWidget.isDragging && layoutWidget.draggingDeviceID != 0 {
		newX := dragEvent.Position.X - layoutWidget.dragOffset.X
		newY := dragEvent.Position.Y - layoutWidget.dragOffset.Y
		minX := layoutWidget.slotMargin + layoutWidget.pcIconSize/2
		maxX := layoutWidget.containerSize.Width - layoutWidget.slotMargin - layoutWidget.pcIconSize/2
		minY := layoutWidget.slotMargin + layoutWidget.pcIconSize/2
		maxY := layoutWidget.containerSize.Height - layoutWidget.slotMargin - layoutWidget.pcIconSize/2
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
		layoutWidget.transientDragPos = fyne.NewPos(newX, newY)
		layoutWidget.Refresh()
	}
}

func (layoutWidget *DeviceStatusLayoutWidget) DragEnd() {
	if !layoutWidget.isDragging || layoutWidget.draggingDeviceID == 0 {
		layoutWidget.isDragging = false
		layoutWidget.draggingDeviceID = 0
		return
	}
	targetSlot := layoutWidget.nearestSlot(layoutWidget.transientDragPos)
	if targetSlot >= 0 {
		currentSlot := layoutWidget.deviceToSlot[layoutWidget.draggingDeviceID]
		if currentSlot != targetSlot {
			otherDeviceID := -1
			for deviceID, slot := range layoutWidget.deviceToSlot {
				if slot == targetSlot {
					otherDeviceID = deviceID
					break
				}
			}
			layoutWidget.deviceToSlot[layoutWidget.draggingDeviceID] = targetSlot
			if otherDeviceID != -1 {
				layoutWidget.deviceToSlot[otherDeviceID] = currentSlot
			}
			layoutWidget.saveDeviceLayout()
		}
	}
	layoutWidget.isDragging = false
	layoutWidget.draggingDeviceID = 0
	layoutWidget.Refresh()
}

func (layoutWidget *DeviceStatusLayoutWidget) positionForDevice(deviceID int) fyne.Position {
	layoutWidget.ensureMapping()
	if layoutWidget.isDragging && layoutWidget.draggingDeviceID == deviceID {
		return layoutWidget.transientDragPos
	}
	slot := layoutWidget.deviceToSlot[deviceID]
	if slot >= 0 && slot < len(layoutWidget.slotPositions) {
		return layoutWidget.slotPositions[slot]
	}
	if len(layoutWidget.slotPositions) == 0 {
		return fyne.NewPos(layoutWidget.slotMargin+layoutWidget.pcIconSize, layoutWidget.slotMargin+layoutWidget.pcIconSize)
	}
	return layoutWidget.slotPositions[slot%len(layoutWidget.slotPositions)]
}

func (layoutWidget *DeviceStatusLayoutWidget) iconSizeForDevice(deviceID int) float32 {
	if deviceID == 17 || deviceID == 18 {
		return layoutWidget.consoleIconSize
	}
	return layoutWidget.pcIconSize
}

type deviceVisual struct {
	icon      *canvas.Image
	primary   *canvas.Text
	secondary *canvas.Text
}

type deviceStatusRenderer struct {
	widget  *DeviceStatusLayoutWidget
	objects []fyne.CanvasObject
	visuals map[int]*deviceVisual
}

func (renderer *deviceStatusRenderer) Layout(size fyne.Size) {
	if renderer.widget.containerSize != size {
		renderer.widget.containerSize = size
		renderer.widget.computeSlots()
	}
	renderer.Refresh()
}

func (renderer *deviceStatusRenderer) MinSize() fyne.Size { return fyne.NewSize(840, 520) }

func firstLast(name string) string {
	parts := strings.Fields(strings.TrimSpace(name))
	if len(parts) >= 2 {
		return parts[0] + " " + parts[1]
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return ""
}

func usersOnDevice(deviceID int) []User {
	users := []User{}
	for _, user := range activeUsers {
		if user.PCID == deviceID {
			users = append(users, user)
		}
	}
	return users
}

func (renderer *deviceStatusRenderer) Refresh() {
	for _, device := range allDevices {
		visual, ok := renderer.visuals[device.ID]
		if !ok {
			visual = renderer.newVisualForDevice(device)
			renderer.visuals[device.ID] = visual
			renderer.objects = append(renderer.objects, visual.icon, visual.primary, visual.secondary)
		}
		renderer.updateVisual(device, visual)
	}
}

func (renderer *deviceStatusRenderer) Objects() []fyne.CanvasObject { return renderer.objects }
func (renderer *deviceStatusRenderer) Destroy()                     {}

func (renderer *deviceStatusRenderer) newVisualForDevice(device Device) *deviceVisual {
	icon := canvas.NewImageFromFile(filepath.Join(imgBaseDir, "free.png"))
	icon.FillMode = canvas.ImageFillContain
	primary := canvas.NewText("", theme.ForegroundColor())
	primary.Alignment = fyne.TextAlignCenter
	secondary := canvas.NewText("", color.NRGBA{A: 255, R: 150, G: 150, B: 160})
	secondary.Alignment = fyne.TextAlignCenter
	secondary.TextSize = 10
	return &deviceVisual{icon: icon, primary: primary, secondary: secondary}
}

func (renderer *deviceStatusRenderer) updateVisual(device Device, visual *deviceVisual) {
	center := renderer.widget.positionForDevice(device.ID)
	size := renderer.widget.iconSizeForDevice(device.ID)
	imageName := renderer.imageNameForDevice(device)
	imagePath := filepath.Join(imgBaseDir, imageName)
	if visual.icon.File != imagePath {
		visual.icon.File = imagePath
	}
	visual.icon.SetMinSize(fyne.NewSize(size, size))
	visual.icon.Resize(fyne.NewSize(size, size))
	visual.icon.Move(fyne.NewPos(center.X-size/2, center.Y-size/2))
	visual.icon.Refresh()

	nameText := ""
	if device.Type == "PC" {
		if device.Status == "occupied" {
			if user := getUserByID(device.UserID); user != nil {
				nameText = firstLast(user.Name)
			}
		}
	} else {
		deviceUsers := usersOnDevice(device.ID)
		if len(deviceUsers) > 0 {
			names := make([]string, 0, len(deviceUsers))
			for _, deviceUser := range deviceUsers {
				names = append(names, firstLast(deviceUser.Name))
			}
			nameText = strings.Join(names, ", ")
		}
	}

	if nameText != "" {
		visual.primary.Text = nameText
		visual.primary.TextStyle = fyne.TextStyle{}
		visual.primary.TextSize = 12
		visual.primary.Refresh()
		visual.primary.Move(fyne.NewPos(center.X-visual.primary.MinSize().Width/2, center.Y+size/2-4))
		visual.primary.Show()

		visual.secondary.Text = fmt.Sprintf("%d", device.ID)
		visual.secondary.Refresh()
		visual.secondary.Move(fyne.NewPos(center.X-visual.secondary.MinSize().Width/2, center.Y+size/2+12))
		visual.secondary.Show()
	} else {
		visual.primary.Text = strconv.Itoa(device.ID)
		visual.primary.TextStyle = fyne.TextStyle{Bold: true}
		visual.primary.TextSize = 12
		visual.primary.Refresh()
		visual.primary.Move(fyne.NewPos(center.X-visual.primary.MinSize().Width/2, center.Y+size/2-4))
		visual.primary.Show()

		visual.secondary.Hide()
	}
}

func (renderer *deviceStatusRenderer) imageNameForDevice(device Device) string {
	if device.Status == "free" {
		if device.Type == "PC" {
			return "free.png"
		}
		return "console.png"
	}
	if device.Type == "PC" {
		return "busy.png"
	}
	return "console_busy.png"
}

func (layoutWidget *DeviceStatusLayoutWidget) CreateRenderer() fyne.WidgetRenderer {
	layoutWidget.computeSlots()
	renderer := &deviceStatusRenderer{
		widget:  layoutWidget,
		visuals: make(map[int]*deviceVisual),
	}
	for _, device := range allDevices {
		visual := renderer.newVisualForDevice(device)
		renderer.visuals[device.ID] = visual
		renderer.objects = append(renderer.objects, visual.icon, visual.primary, visual.secondary)
	}
	return renderer
}

func (layoutWidget *DeviceStatusLayoutWidget) nearestSlot(position fyne.Position) int {
	if len(layoutWidget.slotPositions) == 0 {
		return -1
	}
	bestSlot := -1
	bestDistance := math.MaxFloat64
	for index, slotPosition := range layoutWidget.slotPositions {
		deltaX := float64(slotPosition.X - position.X)
		deltaY := float64(slotPosition.Y - position.Y)
		distance := deltaX*deltaX + deltaY*deltaY
		if distance < bestDistance {
			bestDistance = distance
			bestSlot = index
		}
	}
	return bestSlot
}

func ensureLogDir() error { return os.MkdirAll(logDir, 0o755) }

func getLogFilePath() string {
	return filepath.Join(logDir, fmt.Sprintf("lounge-%s.json", time.Now().Format("2006-01-02")))
}

func readDailyLogEntries() ([]LogEntry, error) {
	p := getLogFilePath()
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return []LogEntry{}, nil
	}
	logFile, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open log: %s: %w", p, err)
	}
	defer logFile.Close()
	fileData, err := io.ReadAll(logFile)
	if err != nil {
		return nil, fmt.Errorf("read log: %s: %w", p, err)
	}
	var entries []LogEntry
	if len(fileData) > 0 {
		if err := json.Unmarshal(fileData, &entries); err != nil {
			return nil, fmt.Errorf("unmarshal log: %s: %w", p, err)
		}
	}
	return entries, nil
}

func writeDailyLogEntries(entries []LogEntry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal log: %w", err)
	}
	return os.WriteFile(getLogFilePath(), data, 0o644)
}

func recordLogEvent(isCheckIn bool, u User, deviceID int, original *time.Time) {
	logFileMutex.Lock()
	defer logFileMutex.Unlock()
	if err := ensureLogDir(); err != nil {
		fmt.Println("Error creating log directory:", err)
		return
	}
	entries, err := readDailyLogEntries()
	if err != nil {
		fmt.Println("Error reading daily log:", err)
		return
	}
	if isCheckIn {
		entries = append(entries, LogEntry{UserName: u.Name, UserID: u.ID, PCID: deviceID, CheckInTime: u.CheckInTime})
	} else {
		found := false
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if e.UserID == u.ID && e.PCID == deviceID && e.CheckOutTime.IsZero() {
				if original == nil || e.CheckInTime.Equal(*original) {
					entries[i].CheckOutTime = time.Now()
					entries[i].UsageTime = formatDuration(entries[i].CheckOutTime.Sub(entries[i].CheckInTime))
					found = true
					break
				}
			}
		}
		if !found {
			fmt.Printf("No matching check-in for user %s (ID: %s) Device %d.\n", u.Name, u.ID, deviceID)
		}
	}
	if err := writeDailyLogEntries(entries); err != nil {
		fmt.Println("Error writing daily log:", err)
	}
	fyne.Do(func() {
		currentLogEntries = entries
		if logTable != nil {
			logTable.Refresh()
		} else {
			logRefreshPending = true
		}
	})
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
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
		func(id widget.TableCellID, o fyne.CanvasObject) {
			l := o.(*widget.Label)
			if id.Row == 0 {
				l.TextStyle.Bold = true
				switch id.Col {
				case 0:
					l.SetText("User Name")
				case 1:
					l.SetText("User ID")
				case 2:
					l.SetText("Device ID")
				case 3:
					l.SetText("Checked In")
				case 4:
					l.SetText("Checked Out")
				case 5:
					l.SetText("Usage Time")
				}
				return
			}
			l.TextStyle.Bold = false
			e := currentLogEntries[id.Row-1]
			switch id.Col {
			case 0:
				l.SetText(e.UserName)
			case 1:
				l.SetText(e.UserID)
			case 2:
				l.SetText(strconv.Itoa(e.PCID))
			case 3:
				l.SetText(e.CheckInTime.Format("15:04:05 (Jan 02)"))
			case 4:
				if e.CheckOutTime.IsZero() {
					l.SetText("-")
				} else {
					l.SetText(e.CheckOutTime.Format("15:04:05 (Jan 02)"))
				}
			case 5:
				l.SetText(e.UsageTime)
			}
		},
	)
	logTable.SetColumnWidth(0, 180)
	logTable.SetColumnWidth(1, 100)
	logTable.SetColumnWidth(2, 70)
	logTable.SetColumnWidth(3, 150)
	logTable.SetColumnWidth(4, 150)
	logTable.SetColumnWidth(5, 120)
	return container.NewScroll(logTable)
}

type leftRatioLayout struct {
	ratio float32
	minW  float32
	maxW  float32
}

func (l *leftRatioLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) == 0 {
		return
	}
	child := objects[0]
	w := size.Width * l.ratio
	if l.minW > 0 && w < l.minW {
		w = l.minW
	}
	if l.maxW > 0 && w > l.maxW {
		w = l.maxW
	}
	child.Resize(fyne.NewSize(w, child.MinSize().Height))
	child.Move(fyne.NewPos(0, 0))
}

func (l *leftRatioLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	if len(objects) == 0 {
		return fyne.NewSize(0, 0)
	}
	min := objects[0].MinSize()
	if l.minW > 0 && min.Width < l.minW {
		return fyne.NewSize(l.minW, min.Height)
	}
	return min
}

type twoPaneLayout struct {
	leftRatio float32
	leftMin   float32
	leftMax   float32
}

func (l *twoPaneLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) < 2 {
		return
	}
	leftWidth := size.Width * l.leftRatio
	if l.leftMin > 0 && leftWidth < l.leftMin {
		leftWidth = l.leftMin
	}
	if l.leftMax > 0 && leftWidth > l.leftMax {
		leftWidth = l.leftMax
	}
	if leftWidth > size.Width {
		leftWidth = size.Width
	}
	left := objects[0]
	right := objects[1]
	left.Resize(fyne.NewSize(leftWidth, size.Height))
	left.Move(fyne.NewPos(0, 0))
	rightWidth := size.Width - leftWidth
	if rightWidth < 0 {
		rightWidth = 0
	}
	right.Resize(fyne.NewSize(rightWidth, size.Height))
	right.Move(fyne.NewPos(leftWidth, 0))
}

func (l *twoPaneLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	if len(objects) < 2 {
		return fyne.NewSize(0, 0)
	}
	leftMin := objects[0].MinSize()
	rightMin := objects[1].MinSize()
	requiredLeft := leftMin.Width
	if l.leftMin > 0 && requiredLeft < l.leftMin {
		requiredLeft = l.leftMin
	}
	width := requiredLeft + rightMin.Width
	height := leftMin.Height
	if rightMin.Height > height {
		height = rightMin.Height
	}
	return fyne.NewSize(width, height)
}

func buildInlineCheckInForm() *fyne.Container {
	checkInNameEntry = widget.NewEntry()
	checkInNameEntry.SetPlaceHolder("Full Name")

	checkInIDEntry = widget.NewEntry()
	checkInIDEntry.SetPlaceHolder("User ID")

	checkInSearchEntry = widget.NewEntry()
	checkInSearchEntry.SetPlaceHolder("Search Member (Name or ID)")

	filteredMembersForInline = nil

	checkInResultsList = widget.NewList(
		func() int { return len(filteredMembersForInline) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i >= 0 && i < len(filteredMembersForInline) {
				m := filteredMembersForInline[i]
				o.(*widget.Label).SetText(fmt.Sprintf("%s (%s)", m.Name, m.ID))
			}
		},
	)
	resultsScroll := container.NewScroll(checkInResultsList)
	resultsScroll.SetMinSize(fyne.NewSize(0, 120))
	resultsScroll.Hide()

	checkInResultsList.OnSelected = func(i widget.ListItemID) {
		if i < 0 || i >= len(filteredMembersForInline) {
			return
		}
		m := filteredMembersForInline[i]
		checkInNameEntry.SetText(m.Name)
		checkInIDEntry.SetText(m.ID)
		checkInSearchEntry.SetText("")
		filteredMembersForInline = nil
		checkInResultsList.UnselectAll()
		checkInResultsList.Refresh()
		resultsScroll.Hide()
		if mainWindow != nil {
			mainWindow.Canvas().Focus(checkInIDEntry)
		}
	}

	checkInSearchEntry.OnChanged = func(q string) {
		q = strings.ToLower(strings.TrimSpace(q))
		if len(members) == 0 {
			loadMembers()
		}
		if q == "" {
			filteredMembersForInline = nil
			checkInResultsList.Refresh()
			resultsScroll.Hide()
			return
		}
		matches := make([]Member, 0, 20)
		for _, m := range members {
			n := strings.ToLower(strings.TrimSpace(m.Name))
			id := strings.ToLower(strings.TrimSpace(m.ID))
			if strings.Contains(n, q) || strings.Contains(id, q) {
				matches = append(matches, m)
			}
		}
		filteredMembersForInline = matches
		checkInResultsList.Refresh()
		if len(matches) > 0 {
			resultsScroll.Show()
		} else {
			resultsScroll.Hide()
		}
	}

	noIDButton := widget.NewButton("No ID?", func() {
		checkInIDEntry.SetText("LOUNGE-" + getNextMemberID())
	})
	addButton := widget.NewButton("Add to Queue", func() {
		name := strings.TrimSpace(checkInNameEntry.Text)
		id := strings.TrimSpace(checkInIDEntry.Text)
		if name == "" || id == "" {
			dialog.ShowError(fmt.Errorf("name and ID are required"), mainWindow)
			return
		}
		if err := registerUser(name, id, 0); err != nil {
			dialog.ShowError(err, mainWindow)
			return
		}
		checkInNameEntry.SetText("")
		checkInIDEntry.SetText("")
		if pendingIconsBox != nil {
			refreshPendingIcons()
		}
	})
	hideButton := widget.NewButton("Hide", func() {
		if checkInInlineForm != nil {
			checkInInlineForm.Hide()
		}
	})

	idRow := container.NewBorder(nil, nil, nil, noIDButton, checkInIDEntry)

	form := widget.NewForm(
		widget.NewFormItem("Name", checkInNameEntry),
		widget.NewFormItem("ID", idRow),
	)

	header := widget.NewLabelWithStyle("Queue Check-In", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	bar := container.NewBorder(nil, nil, nil, hideButton, header)
	card := container.NewVBox(bar, checkInSearchEntry, resultsScroll, form, addButton)

	wrapper := container.New(&leftRatioLayout{ratio: 0.25, minW: 360, maxW: 560}, container.NewPadded(card))
	return wrapper
}

func buildPendingQueueView() fyne.CanvasObject {
	assignmentNoticeLabel = widget.NewLabel("")
	pendingIconsBox = container.NewHBox()
	refreshPendingIcons()

	header := widget.NewLabelWithStyle("Queued Check-Ins", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	scroll := container.NewHScroll(pendingIconsBox)
	scroll.SetMinSize(fyne.NewSize(0, 48))

	return container.NewVBox(header, assignmentNoticeLabel, scroll)
}

func loadMembers() {
	memberHandle, err := os.Open(memberFile)
	if err != nil {
		members = nil
		return
	}
	defer memberHandle.Close()

	memberReader := csv.NewReader(memberHandle)
	memberReader.FieldsPerRecord = -1
	rows, err := memberReader.ReadAll()
	if err != nil || len(rows) == 0 {
		members = nil
		return
	}

	nameIdx, idIdx := -1, -1
	header := rows[0]
	for i := range header {
		key := strings.ToLower(strings.TrimSpace(header[i]))
		if key == "student name" || key == "name" {
			nameIdx = i
		}
		if key == "student number" || key == "id" || key == "student id" {
			idIdx = i
		}
	}

	start := 0
	if nameIdx != -1 && idIdx != -1 {
		start = 1
	} else {
		nameIdx, idIdx = 2, 3
	}

	members = members[:0]
	for _, row := range rows[start:] {
		if nameIdx >= len(row) || idIdx >= len(row) {
			continue
		}
		name := strings.TrimSpace(row[nameIdx])
		id := strings.TrimSpace(row[idIdx])
		if name == "" || id == "" {
			continue
		}
		members = append(members, Member{
			Name:          name,
			ID:            id,
			StudentNumber: id,
		})
	}
}

func getNextMemberID() string { return strconv.Itoa(len(members) + 1) }

func appendMember(member Member) {
	memberHandle, err := os.OpenFile(memberFile, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		fmt.Println("Error opening member file:", err)
		return
	}
	defer memberHandle.Close()

	memberReader := csv.NewReader(memberHandle)
	rows, readErr := memberReader.ReadAll()
	if readErr != nil && readErr != io.EOF {
		fmt.Println("Error reading CSV:", readErr)
		return
	}

	memberHandle.Seek(0, 0)
	memberHandle.Truncate(0)

	memberWriter := csv.NewWriter(memberHandle)
	for _, row := range rows {
		if err := memberWriter.Write(row); err != nil {
			fmt.Println("Error writing row:", err)
			return
		}
	}
	newRow := []string{"", "", member.Name, member.ID}
	if err := memberWriter.Write(newRow); err != nil {
		fmt.Println("Error writing new member:", err)
		return
	}
	memberWriter.Flush()
	if err := memberWriter.Error(); err != nil {
		fmt.Println("Error flushing writer:", err)
	}
	members = append(members, member)
}

func memberByID(id string) *Member {
	for i := range members {
		if members[i].ID == id {
			return &members[i]
		}
	}
	return nil
}

func initData() {
	ensureLogDir()
	allDevices = []Device{}
	for i := 1; i <= 16; i++ {
		allDevices = append(allDevices, Device{ID: i, Type: "PC", Status: "free", UserID: ""})
	}
	allDevices = append(allDevices, Device{ID: 17, Type: "Console", Status: "free", UserID: ""})
	allDevices = append(allDevices, Device{ID: 18, Type: "Console", Status: "free", UserID: ""})

	activeUsers = []User{}
	if _, err := os.Stat(userDataFile); !os.IsNotExist(err) {
		if userDataHandle, openErr := os.Open(userDataFile); openErr == nil {
			defer userDataHandle.Close()
			if json.NewDecoder(userDataHandle).Decode(&activeUsers) == nil {
				for i := range activeUsers {
					userRecord := &activeUsers[i]
					for j := range allDevices {
						if allDevices[j].ID == userRecord.PCID {
							allDevices[j].Status = "occupied"
							if allDevices[j].Type == "PC" {
								allDevices[j].UserID = userRecord.ID
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
}

func saveData() {
	ensureLogDir()
	userDataHandle, err := os.Create(userDataFile)
	if err != nil {
		fmt.Println("Error creating user data file:", err)
		return
	}
	defer userDataHandle.Close()
	if err := json.NewEncoder(userDataHandle).Encode(activeUsers); err != nil {
		fmt.Println("Error encoding user data:", err)
	}
}

func getUserByID(id string) *User {
	for i := range activeUsers {
		if activeUsers[i].ID == id {
			return &activeUsers[i]
		}
	}
	return nil
}

func getDeviceByID(id int) *Device {
	for i := range allDevices {
		if allDevices[i].ID == id {
			return &allDevices[i]
		}
	}
	return nil
}

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
		existing := getUserByID(userID)
		return fmt.Errorf("user ID %s (%s) already checked in on Device %d", userID, existing.Name, existing.PCID)
	}

	if deviceID != 0 {
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
	u := getUserByID(userID)
	if u == nil {
		return fmt.Errorf("user ID %s not found", userID)
	}
	idx := -1
	for i, v := range activeUsers {
		if v.ID == userID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("user %s consistency error", userID)
	}
	originalCheckIn := u.CheckInTime
	devID := u.PCID
	dev := getDeviceByID(devID)

	activeUsers = append(activeUsers[:idx], activeUsers[idx+1:]...)

	if dev != nil {
		if dev.Type == "PC" {
			dev.Status = "free"
			dev.UserID = ""
		} else {
			if len(activeUserIDsOnDevice(dev.ID)) == 0 {
				dev.Status = "free"
			} else {
				dev.Status = "occupied"
			}
		}
	}

	saveData()
	go recordLogEvent(false, *u, devID, &originalCheckIn)
	refreshTrigger <- true
	return nil
}

func removeQueuedUser(userID string) error {
	u := getUserByID(userID)
	if u == nil {
		return fmt.Errorf("user ID %s not found", userID)
	}
	if u.PCID != 0 {
		return fmt.Errorf("user %s is assigned to device %d", userID, u.PCID)
	}
	idx := -1
	for i := range activeUsers {
		if activeUsers[i].ID == userID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("user %s consistency error", userID)
	}
	original := u.CheckInTime
	activeUsers = append(activeUsers[:idx], activeUsers[idx+1:]...)
	saveData()
	go recordLogEvent(false, *u, 0, &original)
	refreshTrigger <- true
	return nil
}

func assignQueuedUserToDevice(userID string, deviceID int) error {
	u := getUserByID(userID)
	if u == nil {
		return fmt.Errorf("user ID %s not found", userID)
	}
	if u.PCID != 0 {
		return fmt.Errorf("user %s already on device %d", userID, u.PCID)
	}
	d := getDeviceByID(deviceID)
	if d == nil {
		return fmt.Errorf("device ID %d does not exist", deviceID)
	}
	if d.Type == "PC" && d.Status != "free" {
		return fmt.Errorf("device %d is busy", deviceID)
	}
	d.Status = "occupied"
	if d.Type == "PC" {
		d.UserID = userID
	}
	original := u.CheckInTime
	u.PCID = deviceID
	saveData()

	logFileMutex.Lock()
	entries, err := readDailyLogEntries()
	if err == nil {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].UserID == userID && entries[i].CheckOutTime.IsZero() &&
				entries[i].PCID == 0 && entries[i].CheckInTime.Equal(original) {
				entries[i].PCID = deviceID
				break
			}
		}
		_ = writeDailyLogEntries(entries)
		currentLogEntries = entries
	}
	logFileMutex.Unlock()

	refreshTrigger <- true
	return nil
}

func getPendingUsers() []User {
	out := []User{}
	for _, u := range activeUsers {
		if u.PCID == 0 {
			out = append(out, u)
		}
	}
	return out
}

func showConsoleCheckoutDialog(d Device) {
	userIDs := activeUserIDsOnDevice(d.ID)
	if len(userIDs) == 0 {
		return
	}
	display := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		u := getUserByID(id)
		name := "Unknown User"
		if u != nil {
			name = u.Name
		}
		display = append(display, fmt.Sprintf("%s (ID: %s)", name, id))
	}
	selector := widget.NewSelectEntry(display)
	items := []*widget.FormItem{{Text: "User on " + d.Type, Widget: selector}}
	dlg := dialog.NewForm("Checkout From "+d.Type, "Check Out", "Cancel", items, func(ok bool) {
		if !ok {
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
	dlg.Resize(fyne.NewSize(420, dlg.MinSize().Height))
	dlg.Show()
}

func buildDeviceRoomContent() fyne.CanvasObject {
	layoutWidget := NewDeviceStatusLayoutWidget()
	layoutWidget.UpdateDevices()

	checkInInlineForm = buildInlineCheckInForm()
	queueView := buildPendingQueueView()

	leftPane := container.NewVBox(
		checkInInlineForm,
		widget.NewSeparator(),
		queueView,
	)
	leftScroll := container.NewVScroll(container.NewPadded(leftPane))
	return container.New(&twoPaneLayout{leftRatio: 0.30, leftMin: 340, leftMax: 520}, leftScroll, layoutWidget)
}

func showCheckInDialogShared(deviceID int, fixed bool) {
	const (
		dialogWidth             float32 = 460
		dialogBaseHeight        float32 = 260
		dialogResultsListHeight float32 = 110
	)

	search := widget.NewEntry()
	search.SetPlaceHolder("Search Existing Member (Name/ID)...")
	nameEntry := widget.NewEntry()
	idEntry := widget.NewEntry()
	deviceEntry := widget.NewEntry()

	nameEntry.SetPlaceHolder("Full Name")
	idEntry.SetPlaceHolder("ID")

	noID := widget.NewButton("No ID?", func() {
		idEntry.SetText("LOUNGE-" + getNextMemberID())
	})
	noID.Resize(fyne.NewSize(55, 25))

	if fixed {
		deviceEntry.SetText(strconv.Itoa(deviceID))
		deviceEntry.Disable()
	} else {
		deviceEntry.SetPlaceHolder("Enter Device ID")
	}

	var filtered []Member
	var results *widget.List
	var dlg dialog.Dialog

	results = widget.NewList(
		func() int { return len(filtered) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i >= 0 && i < len(filtered) {
				o.(*widget.Label).SetText(fmt.Sprintf("%s (%s)", filtered[i].Name, filtered[i].ID))
			}
		})

	scroll := container.NewScroll(results)
	scroll.SetMinSize(fyne.NewSize(dialogWidth-40, dialogResultsListHeight-10))
	scroll.Hide()

	results.OnSelected = func(i widget.ListItemID) {
		if i >= 0 && i < len(filtered) {
			m := filtered[i]
			nameEntry.SetText(m.Name)
			idEntry.SetText(m.ID)
			search.SetText("")
			scroll.Hide()
			results.UnselectAll()
			filtered = []Member{}
			results.Refresh()
			if dlg != nil {
				dlg.Resize(fyne.NewSize(dialogWidth, dialogBaseHeight))
			}
		}
	}

	search.OnChanged = func(s string) {
		q := strings.ToLower(strings.TrimSpace(s))
		if q == "" {
			filtered = []Member{}
		} else {
			out := []Member{}
			for _, m := range members {
				if strings.Contains(strings.ToLower(m.Name), q) || strings.Contains(strings.ToLower(m.ID), q) {
					out = append(out, m)
				}
			}
			filtered = out
		}
		results.Refresh()

		if dlg != nil {
			if len(filtered) > 0 {
				scroll.Show()
				dlg.Resize(fyne.NewSize(dialogWidth, dialogBaseHeight+dialogResultsListHeight))
			} else {
				scroll.Hide()
				dlg.Resize(fyne.NewSize(dialogWidth, dialogBaseHeight))
			}
		}
	}

	userIDRow := container.NewBorder(nil, nil, nil, noID, idEntry)

	form := widget.NewForm(
		widget.NewFormItem("Name:", nameEntry),
		widget.NewFormItem("User ID:", userIDRow),
		widget.NewFormItem("Device ID:", deviceEntry),
	)

	onConfirm := func() {
		uid := strings.TrimSpace(idEntry.Text)
		name := strings.TrimSpace(nameEntry.Text)

		if name == "" || uid == "" {
			dialog.ShowError(fmt.Errorf("name and ID are required"), mainWindow)
			return
		}

		targetDeviceID := 0
		var err error
		if fixed {
			targetDeviceID = deviceID
		} else {
			deviceText := strings.TrimSpace(deviceEntry.Text)
			if deviceText == "" {
				dialog.ShowError(fmt.Errorf("device ID is required"), mainWindow)
				return
			}
			targetDeviceID, err = strconv.Atoi(deviceText)
			if err != nil {
				dialog.ShowError(fmt.Errorf("invalid Device ID: must be a number"), mainWindow)
				return
			}
		}

		if err := registerUser(name, uid, targetDeviceID); err != nil {
			dialog.ShowError(err, mainWindow)
			return
		}
		if dlg != nil {
			dlg.Hide()
		}
	}

	content := container.NewVBox(search, scroll, form)

	dlg = dialog.NewCustomConfirm("Check In User", "Check In", "Cancel", content, func(ok bool) {
		if ok {
			onConfirm()
		}
	}, mainWindow)
	dlg.Resize(fyne.NewSize(dialogWidth, dialogBaseHeight))
	dlg.Show()
}

func showCheckInDialog() { showCheckInDialogShared(0, false) }

func showCheckOutDialog() {
	if len(activeUsers) == 0 {
		dialog.ShowInformation("Check Out", "No active users to check out.", mainWindow)
		return
	}

	display := make([]string, len(activeUsers))
	ids := make([]string, len(activeUsers))

	for i, u := range activeUsers {
		displayName := u.Name
		if len(displayName) > 25 {
			displayName = displayName[:22] + "..."
		}
		display[i] = fmt.Sprintf("%s (ID: %s, PC: %d)", displayName, u.ID, u.PCID)
		ids[i] = u.ID
	}

	selector := widget.NewSelectEntry(display)
	selector.SetPlaceHolder("Select User to Check Out")
	items := []*widget.FormItem{{Text: "User:", Widget: selector}}

	dlg := dialog.NewForm("Check Out User", "Check Out", "Cancel", items, func(ok bool) {
		if !ok {
			return
		}
		choice := strings.TrimSpace(selector.Text)
		if choice == "" {
			dialog.ShowError(fmt.Errorf("no user selected"), mainWindow)
			return
		}
		var target string
		for i, s := range display {
			if s == choice {
				target = ids[i]
				break
			}
		}
		if target == "" {
			dialog.ShowError(fmt.Errorf("invalid user selection"), mainWindow)
			return
		}
		if err := checkoutUser(target); err != nil {
			dialog.ShowError(err, mainWindow)
		}
	}, mainWindow)

	dlg.Resize(fyne.NewSize(450, dlg.MinSize().Height))
	dlg.Show()
}

func main() {
	initData()
	_ = os.MkdirAll(imgBaseDir, 0o755)

	appInstance := app.New()
	appInstance.Settings().SetTheme(NewCatppuccinLatteTheme())
	mainWindow = appInstance.NewWindow("Lounge Management System")
	mainWindow.Resize(fyne.NewSize(1080, 720))

	deviceStatus := buildDeviceRoomContent()
	logView := buildLogView()

	checkInButton := widget.NewButtonWithIcon("Check In", theme.ContentAddIcon(), showCheckInDialog)
	checkOutButton := widget.NewButtonWithIcon("Check Out", theme.ContentRemoveIcon(), showCheckOutDialog)
	toolbar := container.NewHBox(checkInButton, checkOutButton, layout.NewSpacer())

	totalDevicesLabel := widget.NewLabel("")
	activeUsersLabel := widget.NewLabel("")

	updateStatus := func() {
		totalDevicesLabel.SetText(fmt.Sprintf("Total Devices: %d", len(allDevices)))
		activeUsersLabel.SetText(fmt.Sprintf("Active Users: %d", len(activeUsers)))
	}
	updateStatus()

	statusBar := container.NewHBox(totalDevicesLabel, widget.NewLabel(" | "), activeUsersLabel)

	tabs := container.NewAppTabs(
		container.NewTabItem("Device Status", deviceStatus),
		container.NewTabItem("Log", logView),
	)
	tabs.SetTabLocation(container.TabLocationTop)
	tabs.OnSelected = func(it *container.TabItem) {
		if it.Text == "Log" {
			updateCurrentLogEntriesCache()
			if logTable != nil {
				logTable.Refresh()
			}
			logRefreshPending = false
		}
	}

	top := container.NewVBox(toolbar, widget.NewSeparator())
	bottom := container.NewVBox(widget.NewSeparator(), statusBar)
	root := container.NewBorder(top, bottom, nil, nil, tabs)
	mainWindow.SetContent(root)

	go func() {
		logTicker := time.NewTicker(5 * time.Minute)
		lastDate := time.Now().Format("2006-01-02")
		defer logTicker.Stop()

		for {
			select {
			case <-logTicker.C:
				fyne.Do(func() {
					current := time.Now().Format("2006-01-02")
					if current != lastDate {
						lastDate = current
						updateCurrentLogEntriesCache()
						if logTable != nil {
							logTable.Refresh()
						} else {
							logRefreshPending = true
						}
					}
				})
			case <-refreshTrigger:
				fyne.Do(func() {
					updateStatus()
					tabs.Items[0].Content = buildDeviceRoomContent()
					tabs.Refresh()
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

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

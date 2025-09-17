
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

// Small clickable avatar for a queued user.
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
	if w.onAssign != nil {
		w.onAssign(w.user)
	}
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

// ActiveUserNetworkWidget (compact list)
type ActiveUserNetworkWidget struct {
	widget.BaseWidget
	users            []User
	pcPositions      map[string]fyne.Position
	busyPCImage      fyne.Resource
	consoleBusyImage fyne.Resource
	list             *widget.List
}

func NewActiveUserNetworkWidget() *ActiveUserNetworkWidget {
	w := &ActiveUserNetworkWidget{
		users:       make([]User, 0),
		pcPositions: make(map[string]fyne.Position),
	}
	if p := filepath.Join(imgBaseDir, "busy.png"); fileExists(p) {
		w.busyPCImage, _ = fyne.LoadResourceFromPath(p)
	} else {
		w.busyPCImage = theme.ComputerIcon()
	}
	if p := filepath.Join(imgBaseDir, "console_busy.png"); fileExists(p) {
		w.consoleBusyImage, _ = fyne.LoadResourceFromPath(p)
	} else {
		w.consoleBusyImage = w.busyPCImage
	}
	w.ExtendBaseWidget(w)
	return w
}

func (w *ActiveUserNetworkWidget) UpdateUsers(users []User) {
	w.users = append([]User(nil), users...)
	if w.list != nil {
		w.list.Refresh()
	}
	w.Refresh()
}

type activeUserNetworkRenderer struct {
	widget  *ActiveUserNetworkWidget
	objects []fyne.CanvasObject
}

func (r *activeUserNetworkRenderer) buildListItem() fyne.CanvasObject {
	img := canvas.NewImageFromResource(r.widget.busyPCImage)
	img.FillMode = canvas.ImageFillContain
	img.SetMinSize(fyne.NewSize(pcImageSize, pcImageSize))

	line1 := widget.NewLabel("")
	name := widget.NewLabel("")
	name.TextStyle = fyne.TextStyle{Bold: true}
	line3 := widget.NewLabel("")
	line4 := widget.NewLabel("")
	line5 := widget.NewLabel("")

	textCol := container.NewVBox(line1, name, line3, line4, line5)
	return container.NewHBox(img, textCol)
}

func (r *activeUserNetworkRenderer) bindListItem(o fyne.CanvasObject, u User) {
	row := o.(*fyne.Container)
	img := row.Objects[0].(*canvas.Image)
	textCol := row.Objects[1].(*fyne.Container)

	line1 := textCol.Objects[0].(*widget.Label)
	name := textCol.Objects[1].(*widget.Label)
	line3 := textCol.Objects[2].(*widget.Label)
	line4 := textCol.Objects[3].(*widget.Label)
	line5 := textCol.Objects[4].(*widget.Label)

	device := getDeviceByID(u.PCID)
	res := r.widget.busyPCImage
	if device != nil && device.Type == "Console" && r.widget.consoleBusyImage != nil {
		res = r.widget.consoleBusyImage
	}
	img.Resource = res
	img.Refresh()

	line1.SetText(fmt.Sprintf("PC: %d", u.PCID))
	name.SetText(u.Name)
	line3.SetText(fmt.Sprintf("ID: %s", u.ID))
	line4.SetText(fmt.Sprintf("In: %s", u.CheckInTime.Format("15:04:05")))
	line5.SetText(fmt.Sprintf("Up: %s", getFormattedUsageDuration(u.CheckInTime)))
}

func (r *activeUserNetworkRenderer) Layout(size fyne.Size) { r.objects[0].Resize(size) }
func (r *activeUserNetworkRenderer) MinSize() fyne.Size    { return fyne.NewSize(320, 300) }
func (r *activeUserNetworkRenderer) Refresh() {
	if r.widget.list != nil {
		r.widget.list.Refresh()
	}
	canvas.Refresh(r.widget)
}
func (r *activeUserNetworkRenderer) Objects() []fyne.CanvasObject { return r.objects }
func (r *activeUserNetworkRenderer) Destroy()                     {}

func (w *ActiveUserNetworkWidget) CreateRenderer() fyne.WidgetRenderer {
	if w.list == nil {
		r := &activeUserNetworkRenderer{widget: w}
		w.list = widget.NewList(
			func() int { return len(w.users) },
			func() fyne.CanvasObject { return r.buildListItem() },
			func(i widget.ListItemID, o fyne.CanvasObject) {
				if i >= 0 && i < len(w.users) {
					r.bindListItem(o, w.users[i])
				}
			},
		)
		r.objects = []fyne.CanvasObject{w.list}
		return r
	}
	return &activeUserNetworkRenderer{widget: w, objects: []fyne.CanvasObject{w.list}}
}

// Device layout
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
	for id := range w.deviceToSlot {
		seen[id] = true
	}
	order := w.defaultOrder()
	slot := 0
	occ := make(map[int]bool)
	for _, s := range w.deviceToSlot {
		occ[s] = true
	}
	for _, d := range order {
		if !seen[d] {
			for {
				if !occ[slot] {
					w.deviceToSlot[d] = slot
					occ[slot] = true
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
	if w.containerSize.IsZero() {
		return
	}
	w.slotPositions = w.slotPositions[:0]
	total := len(allDevices)
	leftWidth := w.containerSize.Width * 0.75
	leftX := w.slotMargin
	topY := w.slotMargin
	rowHeights := []int{3, 3, 3, 3, 4}
	placed := 0
	for r := 0; r < len(rowHeights) && placed < 16 && placed < total; r++ {
		cols := rowHeights[r]
		rowY := topY + float32(r)*w.slotSpacingY
		rowWidth := float32(cols-1) * w.slotSpacingX
		startX := leftX + (leftWidth-rowWidth)/2
		for c := 0; c < cols && placed < 16 && placed < total; c++ {
			w.slotPositions = append(w.slotPositions, fyne.NewPos(startX+float32(c)*w.slotSpacingX, rowY))
			placed++
		}
	}
	if placed < total {
		rightX := leftWidth + w.slotMargin*2
		y1 := topY + 1*w.slotSpacingY
		y2 := topY + 3*w.slotSpacingY
		w.slotPositions = append(w.slotPositions, fyne.NewPos(rightX, y1))
		if len(w.slotPositions) < total {
			w.slotPositions = append(w.slotPositions, fyne.NewPos(rightX, y2))
		}
	}
	for len(w.slotPositions) < total {
		w.slotPositions = append(w.slotPositions, fyne.NewPos(leftX, topY))
	}
}

func (w *DeviceStatusLayoutWidget) UpdateDevices() { w.ensureMapping(); w.Refresh() }

// Click: checkout/assign/show check-in
func (w *DeviceStatusLayoutWidget) Tapped(ev *fyne.PointEvent) {
	for _, d := range allDevices {
		center := w.positionForDevice(d.ID)
		size := w.iconSizeForDevice(d.ID)
		topLeft := fyne.NewPos(center.X-size/2, center.Y-size/2)

		if ev.Position.X < topLeft.X || ev.Position.X > topLeft.X+size ||
			ev.Position.Y < topLeft.Y || ev.Position.Y > topLeft.Y+size {
			continue
		}

		if d.Status == "occupied" {
			if d.Type == "Console" {
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
				formItems := []*widget.FormItem{{Text: "User on " + d.Type, Widget: selector}}
				dlg := dialog.NewForm("Checkout From "+d.Type, "Check Out", "Cancel", formItems, func(ok bool) {
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
				return
			}

			user := getUserByID(d.UserID)
			name := "Unknown User"
			if user != nil {
				name = user.Name
			}
			dialog.ShowConfirm("Confirm Checkout", fmt.Sprintf("Checkout %s from %s %d?", name, d.Type, d.ID),
				func(ok bool) {
					if ok {
						if err := checkoutUser(d.UserID); err != nil {
							dialog.ShowError(err, mainWindow)
						}
					}
				}, mainWindow)
			return
		}

		if assignmentUserID != "" { // assign pending user to this free device
			target := assignmentUserID
			assignmentUserID = ""
			if assignmentNoticeLabel != nil {
				assignmentNoticeLabel.SetText("")
			}
			if err := assignQueuedUserToDevice(target, d.ID); err != nil {
				dialog.ShowError(err, mainWindow)
			}
			return
		}

		showCheckInDialogShared(d.ID, true)
		return
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

func (w *DeviceStatusLayoutWidget) positionForDevice(id int) fyne.Position {
	w.ensureMapping()
	slot := w.deviceToSlot[id]
	if slot >= 0 && slot < len(w.slotPositions) {
		if w.isDragging && w.draggingDeviceID == id {
			return w.transientDragPos
		}
		return w.slotPositions[slot]
	}
	if len(w.slotPositions) == 0 {
		return fyne.NewPos(w.slotMargin+w.pcIconSize, w.slotMargin+w.pcIconSize)
	}
	return w.slotPositions[slot%len(w.slotPositions)]
}

func (w *DeviceStatusLayoutWidget) iconSizeForDevice(id int) float32 {
	if id == 17 || id == 18 {
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

func (r *deviceStatusRenderer) MinSize() fyne.Size { return fyne.NewSize(700, 420) }

func (r *deviceStatusRenderer) Refresh() {
	r.objects = r.objects[:0]
	for _, d := range allDevices {
		center := r.widget.positionForDevice(d.ID)
		size := r.widget.iconSizeForDevice(d.ID)

		var base string
		if d.Status == "free" {
			if d.Type == "PC" {
				base = "free.png"
			} else {
				base = "console.png"
			}
		} else {
			if d.Type == "PC" {
				base = "busy.png"
			} else {
				base = "console_busy.png"
			}
		}
		imagePath := filepath.Join(imgBaseDir, base)
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

// Logs & members
func ensureLogDir() error { return os.MkdirAll(logDir, 0o755) }

func getLogFilePath() string {
	return filepath.Join(logDir, fmt.Sprintf("lounge-%s.json", time.Now().Format("2006-01-02")))
}

func readDailyLogEntries() ([]LogEntry, error) {
	p := getLogFilePath()
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return []LogEntry{}, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open log: %s: %w", p, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read log: %s: %w", p, err)
	}
	var entries []LogEntry
	if len(b) > 0 {
		if err := json.Unmarshal(b, &entries); err != nil {
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
		if tabs != nil && tabs.Selected() != nil && tabs.Selected().Text == "Log" {
			if logTable != nil {
				logTable.Refresh()
			}
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

// Left-anchored responsive layout (child width = ratio * parent, clamped by min/max).
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

// Inline check-in with membership search.
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

	// Left anchored, responsive width (25% of window; 360-560px clamps)
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

// Members CSV
func loadMembers() {
	f, err := os.Open(memberFile)
	if err != nil {
		members = nil
		return
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
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

func appendMember(m Member) {
	f, err := os.OpenFile(memberFile, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		fmt.Println("Error opening member file:", err)
		return
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, readErr := r.ReadAll()
	if readErr != nil && readErr != io.EOF {
		fmt.Println("Error reading CSV:", readErr)
		return
	}

	f.Seek(0, 0)
	f.Truncate(0)

	w := csv.NewWriter(f)
	for _, row := range rows {
		if err := w.Write(row); err != nil {
			fmt.Println("Error writing row:", err)
			return
		}
	}
	newRow := []string{"", "", m.Name, m.ID}
	if err := w.Write(newRow); err != nil {
		fmt.Println("Error writing new member:", err)
		return
	}
	w.Flush()
	if err := w.Error(); err != nil {
		fmt.Println("Error flushing writer:", err)
	}
	members = append(members, m)
}

func memberByID(id string) *Member {
	for i := range members {
		if members[i].ID == id {
			return &members[i]
		}
	}
	return nil
}

// Data init & helpers
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
		if f, e := os.Open(userDataFile); e == nil {
			defer f.Close()
			if json.NewDecoder(f).Decode(&activeUsers) == nil {
				for i := range activeUsers {
					u := &activeUsers[i]
					for j := range allDevices {
						if allDevices[j].ID == u.PCID {
							allDevices[j].Status = "occupied"
							if allDevices[j].Type == "PC" {
								allDevices[j].UserID = u.ID
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
	f, err := os.Create(userDataFile)
	if err != nil {
		fmt.Println("Error creating user data file:", err)
		return
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(activeUsers); err != nil {
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

func getDeviceByUserID(id string) *Device {
	for i := range allDevices {
		if allDevices[i].UserID == id {
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

	if activeUserNetworkInstance != nil {
		delete(activeUserNetworkInstance.pcPositions, userID)
	}

	saveData()
	go recordLogEvent(false, *u, devID, &originalCheckIn)
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
	if d.Status != "free" {
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

func getFormattedUsageDuration(start time.Time) string { return formatDuration(time.Since(start)) }

// Device button (legacy hover info)
type DeviceButton struct {
	widget.BaseWidget
	device *Device
}

func NewDeviceButton(d *Device) *DeviceButton {
	b := &DeviceButton{device: d}
	b.ExtendBaseWidget(b)
	return b
}

func (b *DeviceButton) Tapped(_ *fyne.PointEvent) {
	if b.device.Status == "occupied" {
		if b.device.Type == "Console" {
			userIDs := activeUserIDsOnDevice(b.device.ID)
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
			items := []*widget.FormItem{{Text: "User on " + b.device.Type, Widget: selector}}
			dlg := dialog.NewForm("Checkout From "+b.device.Type, "Check Out", "Cancel", items, func(ok bool) {
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
			return
		}

		u := getUserByID(b.device.UserID)
		name := "Unknown User"
		if u != nil {
			name = u.Name
		}
		dialog.ShowConfirm("Confirm Checkout", fmt.Sprintf("Checkout %s from %s %d?", name, b.device.Type, b.device.ID),
			func(ok bool) {
				if ok {
					if err := checkoutUser(b.device.UserID); err != nil {
						dialog.ShowError(err, mainWindow)
					}
				}
			}, mainWindow)
	} else {
		showCheckInDialogShared(b.device.ID, true)
	}
}

func (b *DeviceButton) MouseIn(_ *desktop.MouseEvent) {
	if deviceHoverDetailLabel == nil {
		return
	}
	if b.device.Status != "occupied" {
		deviceHoverDetailLabel.SetText("")
		return
	}
	if b.device.Type == "Console" {
		userIDs := activeUserIDsOnDevice(b.device.ID)
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
		deviceHoverDetailLabel.SetText(fmt.Sprintf("%s %d:\n%s", b.device.Type, b.device.ID, strings.Join(lines, "\n")))
		return
	}
	u := getUserByID(b.device.UserID)
	if u != nil {
		dev := getDeviceByID(u.PCID)
		typ := "Unknown Device"
		if dev != nil {
			typ = dev.Type
		}
		deviceHoverDetailLabel.SetText(fmt.Sprintf("Using %s %d: %s (ID: %s)\nChecked In: %s  |  Usage: %s",
			typ, u.PCID, u.Name, u.ID, u.CheckInTime.Format("15:04:05 (Jan 02)"), getFormattedUsageDuration(u.CheckInTime)))
	} else {
		deviceHoverDetailLabel.SetText(fmt.Sprintf("%s %d: User details not found (UserID: %s).", b.device.Type, b.device.ID, b.device.UserID))
	}
}

func (b *DeviceButton) MouseOut() {
	if deviceHoverDetailLabel != nil {
		deviceHoverDetailLabel.SetText("")
	}
}
func (b *DeviceButton) Dragged(_ *fyne.DragEvent) {}
func (b *DeviceButton) DragEnd()                  {}
func (b *DeviceButton) CreateRenderer() fyne.WidgetRenderer {
	base := "free.png"
	if b.device.Type == "Console" {
		base = "console.png"
	}
	imagePath := filepath.Join(imgBaseDir, base)
	img := canvas.NewImageFromFile(imagePath)
	img.FillMode = canvas.ImageFillContain
	img.SetMinSize(fyne.NewSize(64, 64))

	text := strconv.Itoa(b.device.ID)
	if b.device.ID == 17 {
		text = "Xbox " + text
	}
	if b.device.ID == 18 {
		text = "PS4 " + text
	}
	lbl := widget.NewLabel(text)
	lbl.TextStyle = fyne.TextStyle{Bold: true}
	lbl.Alignment = fyne.TextAlignCenter

	content := container.NewVBox(img, lbl)
	r := &deviceRenderer{button: b, image: img, label: lbl, objects: []fyne.CanvasObject{content}}
	r.Refresh()
	return r
}

type deviceRenderer struct {
	button  *DeviceButton
	image   *canvas.Image
	label   *widget.Label
	objects []fyne.CanvasObject
}

func (r *deviceRenderer) Layout(size fyne.Size)        { r.objects[0].Resize(size) }
func (r *deviceRenderer) MinSize() fyne.Size           { return r.objects[0].MinSize() }
func (r *deviceRenderer) Objects() []fyne.CanvasObject { return r.objects }
func (r *deviceRenderer) Destroy()                     {}
func (r *deviceRenderer) BackgroundColor() color.Color { return color.Transparent }
func (r *deviceRenderer) Refresh() {
	var base string
	if r.button.device.Status == "free" {
		if r.button.device.Type == "PC" {
			base = "free.png"
		} else {
			base = "console.png"
		}
	} else {
		if r.button.device.Type == "PC" {
			base = "busy.png"
		} else {
			base = "console_busy.png"
		}
	}
	imagePath := filepath.Join(imgBaseDir, base)
	r.image.File = imagePath
	r.image.Refresh()

	text := strconv.Itoa(r.button.device.ID)
	if r.button.device.ID == 17 {
		text = "Xbox " + text
	}
	if r.button.device.ID == 18 {
		text = "PS4 " + text
	}
	r.label.SetText(text)
	r.label.Refresh()
	canvas.Refresh(r.button)
}

// Device room (left: devices, right: active users, bottom: queue)
func buildDeviceRoomContent() fyne.CanvasObject {
	layoutWidget := NewDeviceStatusLayoutWidget()
	layoutWidget.UpdateDevices()

	if deviceHoverDetailLabel == nil {
		deviceHoverDetailLabel = widget.NewLabel("")
		deviceHoverDetailLabel.Wrapping = fyne.TextWrapWord
		deviceHoverDetailLabel.Alignment = fyne.TextAlignCenter
	}

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
	split.Offset = 0.68

	checkInInlineForm = buildInlineCheckInForm()
	checkInInlineForm.Hide()

	queueView := buildPendingQueueView()

	bottom := container.NewVBox(
		checkInInlineForm,
		widget.NewSeparator(),
		queueView,
	)
	return container.NewBorder(nil, bottom, nil, nil, split)
}

// Dialog check-in (still available when clicking free device)
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

func showCheckInDialog() {
	if tabs != nil && deviceStatusTabIndex != -1 {
		tabs.Select(tabs.Items[deviceStatusTabIndex])
	}
	if checkInInlineForm != nil {
		checkInInlineForm.Show()
		if mainWindow != nil && checkInNameEntry != nil {
			mainWindow.Canvas().Focus(checkInNameEntry)
		}
	}
}

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

// Main
func main() {
	initData()
	_ = os.MkdirAll(imgBaseDir, 0o755)

	fapp := app.New()
	fapp.Settings().SetTheme(NewCatppuccinLatteTheme())
	mainWindow = fapp.NewWindow("Lounge Management System")
	mainWindow.Resize(fyne.NewSize(1080, 720))

	deviceHoverDetailLabel = widget.NewLabel("")
	deviceHoverDetailLabel.Wrapping = fyne.TextWrapWord
	deviceHoverDetailLabel.Alignment = fyne.TextAlignCenter

	activeUserNetworkInstance = NewActiveUserNetworkWidget()

	deviceStatus := buildDeviceRoomContent()
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

	updateStatus := func() {
		totalDevicesLabel.SetText(fmt.Sprintf("Total Devices: %d", len(allDevices)))
		activeUsersLabel.SetText(fmt.Sprintf("Active Users: %d", len(activeUsers)))
	}
	updateStatus()

	statusBar := container.NewHBox(totalDevicesLabel, widget.NewLabel(" | "), activeUsersLabel)

	tabs = container.NewAppTabs(
		container.NewTabItem("Device Status", deviceStatus),
		container.NewTabItem("Log", logView),
	)

	for i, it := range tabs.Items {
		switch it.Text {
		case "Device Status":
			deviceStatusTabIndex = i
		case "Log":
			logTabIndex = i
		}
	}

	tabs.SetTabLocation(container.TabLocationTop)
	tabs.OnSelected = func(it *container.TabItem) {
		resetLayoutButton.Hide()
		if it.Text == "Log" && logTabIndex != -1 {
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
		uiTicker := time.NewTicker(time.Second)
		logTicker := time.NewTicker(5 * time.Minute)
		lastDate := time.Now().Format("2006-01-02")
		defer uiTicker.Stop()
		defer logTicker.Stop()

		for {
			select {
			case <-uiTicker.C:
				fyne.Do(func() {
					if tabs.Selected() != nil && tabs.Selected().Text == "Device Status" {
						if activeUserNetworkInstance != nil {
							activeUserNetworkInstance.Refresh()
						}
					}
				})
			case <-logTicker.C:
				fyne.Do(func() {
					current := time.Now().Format("2006-01-02")
					if current != lastDate {
						lastDate = current
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
					updateStatus()
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

// utilities
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

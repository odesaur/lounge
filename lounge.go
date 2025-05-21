package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"math"
	"math/rand"
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
	userDataFile = "log/active_users.json"
	memberFile   = "membership.csv"
	logDir       = "log"
	imgBaseDir   = "src"

	pcImageSize       float32 = 48
	infoBoxWidth      float32 = 180
	infoBoxHeight     float32 = 90
	initialRingRadius float32 = 100
	ringStep          float32 = 120
	canvasPadding     float32 = 20
	nodeVisualWidth   float32 = pcImageSize + infoBoxWidth + 25
	nodeVisualHeight  float32 = infoBoxHeight + 20
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
	Name string
	ID   string
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
	displayMembers            []Member
	mainWindow                fyne.Window
	memberTable               *widget.Table
	logTable                  *widget.Table
	refreshTrigger            = make(chan bool, 1)
	logRefreshPending         = false
	logFileMutex              sync.Mutex
	currentLogEntries         []LogEntry
	memberSearchEntry         *widget.Entry
	tabs                      *container.AppTabs
	deviceStatusTabIndex      int = -1
	activeUsersTabIndex       int = -1
	membershipTabIndex        int = -1
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
	classicDisabledText := color.NRGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xFF}
	classicInputBorder := color.NRGBA{R: 0x40, G: 0x40, B: 0x40, A: 0xFF}
	classicShadow := color.NRGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xFF}

	switch name {
	case theme.ColorNameBackground:
		return classicBackground
	case theme.ColorNameButton:
		return classicButtonFace
	case theme.ColorNamePrimary:
		return classicPrimary
	case theme.ColorNameHover:
		return color.NRGBA{R: 0xB0, G: 0xB0, B: 0xD0, A: 0xFF}
	case theme.ColorNameFocus:
		return classicPrimary
	case theme.ColorNameSelection:
		return classicPrimary
	case theme.ColorNameShadow:
		return classicShadow
	case theme.ColorNameInputBorder:
		return classicInputBorder
	case theme.ColorNameDisabled:
		return classicDisabledText
	case theme.ColorNamePlaceHolder:
		return classicDisabledText
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

type ActiveUserNetworkWidget struct {
	widget.BaseWidget
	users            []User
	pcPositions      map[string]fyne.Position
	rng              *rand.Rand
	busyPCImage      fyne.Resource
	consoleBusyImage fyne.Resource // Added for console-specific busy image
	containerSize    fyne.Size
	draggingUserID   string
	dragOffset       fyne.Position
	isDragging       bool
}

func NewActiveUserNetworkWidget() *ActiveUserNetworkWidget {
	w := &ActiveUserNetworkWidget{
		users:       make([]User, 0),
		pcPositions: make(map[string]fyne.Position),
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	busyPCPath := filepath.Join(imgBaseDir, "busy.png")
	if _, err := os.Stat(busyPCPath); err == nil {
		w.busyPCImage, _ = fyne.LoadResourceFromPath(busyPCPath)
	} else {
		fmt.Printf("Warning: busy.png not found at %s. Using fallback.\n", busyPCPath)
		w.busyPCImage = theme.ComputerIcon()
	}

	consoleBusyPath := filepath.Join(imgBaseDir, "console_busy.png")
	if _, err := os.Stat(consoleBusyPath); err == nil {
		w.consoleBusyImage, _ = fyne.LoadResourceFromPath(consoleBusyPath)
	} else {
		fmt.Printf("Warning: console_busy.png not found at %s. Using busyPCImage as fallback for consoles.\n", consoleBusyPath)
		w.consoleBusyImage = w.busyPCImage // Fallback to the general busy image if console specific one is missing
	}

	w.ExtendBaseWidget(w)
	return w
}

func (w *ActiveUserNetworkWidget) UpdateUsers(users []User) {
	newUsersMap := make(map[string]User)
	for _, u := range users {
		newUsersMap[u.ID] = u
	}

	existingUserIDsInWidget := make(map[string]bool)
	for _, u := range w.users {
		existingUserIDsInWidget[u.ID] = true
	}

	usersActuallyChanged := false
	if len(users) != len(w.users) {
		usersActuallyChanged = true
	} else {
		for _, u := range users {
			if _, exists := existingUserIDsInWidget[u.ID]; !exists {
				usersActuallyChanged = true
				break
			}
		}
		if !usersActuallyChanged {
			for oldID := range existingUserIDsInWidget {
				if _, exists := newUsersMap[oldID]; !exists {
					usersActuallyChanged = true
					break
				}
			}
		}
	}

	for userID := range w.pcPositions {
		if _, exists := newUsersMap[userID]; !exists {
			delete(w.pcPositions, userID)
			usersActuallyChanged = true
		}
	}

	w.users = make([]User, len(users))
	copy(w.users, users)

	newUsersAdded := false
	for _, user := range w.users {
		if _, exists := w.pcPositions[user.ID]; !exists {
			newUsersAdded = true
			break
		}
	}

	if usersActuallyChanged || newUsersAdded || (len(w.users) > 0 && len(w.pcPositions) == 0) {
		w.calculatePCPositions()
	}
	w.Refresh()
}

func (w *ActiveUserNetworkWidget) calculatePCPositions() {
	if w.containerSize.IsZero() {
		return
	}
	if len(w.users) == 0 {
		w.pcPositions = make(map[string]fyne.Position)
		return
	}

	center := fyne.NewPos(w.containerSize.Width/2, w.containerSize.Height/2)

	usersToPlace := []User{}
	for _, user := range w.users {
		if _, exists := w.pcPositions[user.ID]; !exists {
			usersToPlace = append(usersToPlace, user)
		}
	}

	if len(usersToPlace) == 0 {
		return
	}

	if len(w.users) == 1 && len(usersToPlace) == 1 {
		w.pcPositions[usersToPlace[0].ID] = center
		return
	}

	if len(w.users) > 0 {
		firstUserID := w.users[0].ID
		// Check if the first user is in the list of users that need placing
		needsPlacing := false
		for _, u := range usersToPlace {
			if u.ID == firstUserID {
				needsPlacing = true
				break
			}
		}
		// If the first user exists, needs placing, AND doesn't already have a position (e.g., after reset)
		if _, alreadyHasPos := w.pcPositions[firstUserID]; !alreadyHasPos && needsPlacing {
			w.pcPositions[firstUserID] = center
			tempUsersToPlace := []User{}
			for _, u := range usersToPlace {
				if u.ID != firstUserID {
					tempUsersToPlace = append(tempUsersToPlace, u)
				}
			}
			usersToPlace = tempUsersToPlace
		}
	}
	if len(usersToPlace) == 0 {
		return
	}

	currentRadius := initialRingRadius
	if len(w.pcPositions) > 1 {
		maxR := float32(0.0)
		for _, pos := range w.pcPositions {
			if pos.IsZero() {
				continue
			}
			distX := pos.X - center.X
			distY := pos.Y - center.Y
			r := float32(math.Sqrt(float64(distX*distX + distY*distY)))
			if r > maxR {
				maxR = r
			}
		}
		if maxR > 0 && maxR+ringStep > currentRadius {
			currentRadius = maxR + ringStep
		}
	}

	nodesPlacedOnCurrentRing := 0
	currentRingCapacity := int(math.Max(1.0, (2*math.Pi*float64(currentRadius))/(float64(nodeVisualWidth)*0.95)))
	if currentRingCapacity == 0 {
		currentRingCapacity = 1
	}
	initialAngleOffset := w.rng.Float64() * (math.Pi / float64(currentRingCapacity))

	tempNewPositions := make(map[string]fyne.Position)

	for _, user := range usersToPlace {
		if nodesPlacedOnCurrentRing >= currentRingCapacity {
			currentRadius += ringStep
			nodesPlacedOnCurrentRing = 0
			circumference := 2 * math.Pi * float64(currentRadius)
			currentRingCapacity = int(math.Max(1.0, circumference/(float64(nodeVisualWidth)*0.95)))
			if currentRingCapacity == 0 {
				currentRingCapacity = 1
			}
			initialAngleOffset = w.rng.Float64() * (math.Pi / float64(currentRingCapacity))
		}

		angle := initialAngleOffset + (2*math.Pi/float64(currentRingCapacity))*float64(nodesPlacedOnCurrentRing)
		posX := center.X + currentRadius*float32(math.Cos(angle))
		posY := center.Y + currentRadius*float32(math.Sin(angle))

		minIconCenterX := canvasPadding + pcImageSize/2
		if posX-pcImageSize/2-8-infoBoxWidth < canvasPadding {
			minIconCenterX = canvasPadding + infoBoxWidth + 8 + pcImageSize/2
		}
		maxIconCenterX := w.containerSize.Width - canvasPadding - pcImageSize/2
		if posX+pcImageSize/2+8+infoBoxWidth > w.containerSize.Width-canvasPadding {
			maxIconCenterX = w.containerSize.Width - canvasPadding - infoBoxWidth - 8 - pcImageSize/2
		}
		posX = float32(math.Max(float64(minIconCenterX), math.Min(float64(posX), float64(maxIconCenterX))))

		minIconCenterY := canvasPadding + infoBoxHeight/2
		maxIconCenterY := w.containerSize.Height - canvasPadding - infoBoxHeight/2
		posY = float32(math.Max(float64(minIconCenterY), math.Min(float64(posY), float64(maxIconCenterY))))

		finalPos := fyne.NewPos(posX, posY)

		attempts := 0
		maxAttemptsToAdjust := 10
		for attempts < maxAttemptsToAdjust {
			isOverlapping := false
			for _, existingPos := range w.pcPositions {
				dist := math.Sqrt(math.Pow(float64(existingPos.X-finalPos.X), 2) + math.Pow(float64(existingPos.Y-finalPos.Y), 2))
				if dist < float64(nodeVisualWidth*0.70) {
					isOverlapping = true
					break
				}
			}
			if !isOverlapping {
				for _, newlyPlacedPos := range tempNewPositions {
					dist := math.Sqrt(math.Pow(float64(newlyPlacedPos.X-finalPos.X), 2) + math.Pow(float64(newlyPlacedPos.Y-finalPos.Y), 2))
					if dist < float64(nodeVisualWidth*0.70) {
						isOverlapping = true
						break
					}
				}
			}

			if !isOverlapping {
				break
			}

			if attempts%2 == 0 {
				angle += (math.Pi / float64(currentRingCapacity*(2+attempts/2)))
			} else {
				currentRadius += ringStep * 0.05
			}
			posX = center.X + currentRadius*float32(math.Cos(angle))
			posY = center.Y + currentRadius*float32(math.Sin(angle))

			posX = float32(math.Max(float64(minIconCenterX), math.Min(float64(posX), float64(maxIconCenterX))))
			posY = float32(math.Max(float64(minIconCenterY), math.Min(float64(posY), float64(maxIconCenterY))))
			finalPos = fyne.NewPos(posX, posY)
			attempts++
		}

		w.pcPositions[user.ID] = finalPos
		tempNewPositions[user.ID] = finalPos
		nodesPlacedOnCurrentRing++
	}
}

func (w *ActiveUserNetworkWidget) Dragged(event *fyne.DragEvent) {
	if !w.isDragging {
		for _, user := range w.users {
			pcPos, ok := w.pcPositions[user.ID]
			if !ok {
				continue
			}
			iconTopLeftX := pcPos.X - pcImageSize/2
			iconTopLeftY := pcPos.Y - pcImageSize/2

			if event.Position.X >= iconTopLeftX && event.Position.X <= iconTopLeftX+pcImageSize &&
				event.Position.Y >= iconTopLeftY && event.Position.Y <= iconTopLeftY+pcImageSize {

				w.isDragging = true
				w.draggingUserID = user.ID
				w.dragOffset = fyne.NewPos(event.Position.X-pcPos.X, event.Position.Y-pcPos.Y)
				break
			}
		}
	}

	if w.isDragging && w.draggingUserID != "" {
		newPcPosX := event.Position.X - w.dragOffset.X
		newPcPosY := event.Position.Y - w.dragOffset.Y

		newPcPosX = float32(math.Max(float64(canvasPadding+pcImageSize/2), math.Min(float64(newPcPosX), float64(w.containerSize.Width-canvasPadding-pcImageSize/2))))
		newPcPosY = float32(math.Max(float64(canvasPadding+pcImageSize/2), math.Min(float64(newPcPosY), float64(w.containerSize.Height-canvasPadding-pcImageSize/2))))

		w.pcPositions[w.draggingUserID] = fyne.NewPos(newPcPosX, newPcPosY)
		w.Refresh()
	}
}

func (w *ActiveUserNetworkWidget) DragEnd() {
	w.isDragging = false
	w.draggingUserID = ""
}

type activeUserNetworkRenderer struct {
	widget  *ActiveUserNetworkWidget
	objects []fyne.CanvasObject
}

func (r *activeUserNetworkRenderer) Layout(size fyne.Size) {
	if r.widget.containerSize != size {
		oldSize := r.widget.containerSize
		r.widget.containerSize = size
		if oldSize.IsZero() || math.Abs(float64(oldSize.Width-size.Width)) > 50 || math.Abs(float64(oldSize.Height-size.Height)) > 50 {
			r.widget.calculatePCPositions()
		}
	}
	r.widget.Refresh()
}

func (r *activeUserNetworkRenderer) MinSize() fyne.Size {
	return fyne.NewSize(600, 400)
}

func (r *activeUserNetworkRenderer) Refresh() {
	r.objects = []fyne.CanvasObject{}

	if r.widget.busyPCImage == nil {
		r.objects = append(r.objects, widget.NewLabel("Error: busy.png resource not loaded"))
		canvas.Refresh(r.widget)
		return
	}

	if len(r.widget.users) == 0 {
		noUsersLabel := widget.NewLabel("No active users to display.")
		if !r.widget.containerSize.IsZero() {
			labelSize := noUsersLabel.MinSize()
			noUsersLabel.Move(fyne.NewPos(
				(r.widget.containerSize.Width-labelSize.Width)/2,
				(r.widget.containerSize.Height-labelSize.Height)/2,
			))
		}
		r.objects = append(r.objects, noUsersLabel)
		canvas.Refresh(r.widget)
		return
	}

	var centerUserPos fyne.Position
	centerUserExists := false
	if len(r.widget.users) > 0 {
		firstUserID := r.widget.users[0].ID
		if pos, ok := r.widget.pcPositions[firstUserID]; ok {
			centerUserPos = pos
			centerUserExists = true
		}
	}

	for i, user := range r.widget.users {
		if i == 0 && centerUserExists {
			continue
		}
		pcPos, ok := r.widget.pcPositions[user.ID]
		if !ok {
			continue
		}
		if centerUserExists {
			line := canvas.NewLine(theme.PrimaryColor())
			line.Position1 = pcPos
			line.Position2 = centerUserPos
			line.StrokeWidth = 1.5
			r.objects = append(r.objects, line)
		}
	}

	for _, user := range r.widget.users {
		pcPos, ok := r.widget.pcPositions[user.ID]
		if !ok {
			// Silently skip drawing if position is missing
			// fmt.Printf("DEBUG: Position for user %s (ID: %s) not found. Skipping draw.\n", user.Name, user.ID)
			continue
		}

		var currentBusyImage fyne.Resource = r.widget.busyPCImage
		device := getDeviceByID(user.PCID)
		if device != nil && device.Type == "Console" {
			if r.widget.consoleBusyImage != nil {
				currentBusyImage = r.widget.consoleBusyImage
			}
		}

		pcIcon := canvas.NewImageFromResource(currentBusyImage)
		pcIcon.Resize(fyne.NewSize(pcImageSize, pcImageSize))
		pcIcon.Move(fyne.NewPos(pcPos.X-pcImageSize/2, pcPos.Y-pcImageSize/2))
		r.objects = append(r.objects, pcIcon)

		pcNumText := canvas.NewText(strconv.Itoa(user.PCID), color.White)
		pcNumText.TextSize = 12
		pcNumText.Alignment = fyne.TextAlignCenter
		pcNumText.TextStyle.Bold = true
		pcNumText.Move(fyne.NewPos(pcPos.X-pcNumText.MinSize().Width/2, pcPos.Y-pcNumText.MinSize().Height/2))
		r.objects = append(r.objects, pcNumText)

		infoBoxRect := canvas.NewRectangle(theme.BackgroundColor())
		infoBoxRect.SetMinSize(fyne.NewSize(infoBoxWidth, infoBoxHeight))

		infoBoxX := pcPos.X + pcImageSize/2 + 8
		infoBoxY := pcPos.Y - infoBoxHeight/2

		if !r.widget.containerSize.IsZero() {
			if infoBoxX+infoBoxWidth > r.widget.containerSize.Width-canvasPadding {
				infoBoxX = pcPos.X - pcImageSize/2 - infoBoxWidth - 8
			}
			if infoBoxX < canvasPadding {
				infoBoxX = canvasPadding
			}
			if infoBoxY < canvasPadding {
				infoBoxY = canvasPadding
			}
			if infoBoxY+infoBoxHeight > r.widget.containerSize.Height-canvasPadding {
				infoBoxY = r.widget.containerSize.Height - infoBoxHeight - canvasPadding
			}
		}

		infoBoxRect.Move(fyne.NewPos(infoBoxX, infoBoxY))
		infoBoxRect.StrokeColor = theme.PrimaryColor()
		infoBoxRect.StrokeWidth = 1
		r.objects = append(r.objects, infoBoxRect)

		line1 := fmt.Sprintf("PC: %d", user.PCID)
		line2 := fmt.Sprintf("%s", user.Name)
		line3 := fmt.Sprintf("ID: %s", user.ID)
		line4 := fmt.Sprintf("In: %s", user.CheckInTime.Format("15:04:05"))
		line5 := fmt.Sprintf("Up: %s", getFormattedUsageDuration(user.CheckInTime))

		texts := []*canvas.Text{
			canvas.NewText(line1, theme.ForegroundColor()),
			canvas.NewText(line2, theme.ForegroundColor()),
			canvas.NewText(line3, theme.ForegroundColor()),
			canvas.NewText(line4, theme.ForegroundColor()),
			canvas.NewText(line5, theme.ForegroundColor()),
		}

		currentY := infoBoxY + 3
		for textIdx, txt := range texts {
			txt.TextSize = 10
			if textIdx == 1 {
				txt.TextSize = 11
				txt.TextStyle.Bold = true
			}
			txt.Move(fyne.NewPos(infoBoxX+5, currentY))
			r.objects = append(r.objects, txt)
			currentY += txt.MinSize().Height + 1
		}
	}
	canvas.Refresh(r.widget)
}

func (r *activeUserNetworkRenderer) Objects() []fyne.CanvasObject {
	return r.objects
}
func (r *activeUserNetworkRenderer) Destroy() {}

func (w *ActiveUserNetworkWidget) CreateRenderer() fyne.WidgetRenderer {
	r := &activeUserNetworkRenderer{
		widget:  w,
		objects: []fyne.CanvasObject{},
	}
	return r
}

func ensureLogDir() error { return os.MkdirAll(logDir, 0755) }
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
	return os.WriteFile(filePath, fileData, 0644)
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

func formatDuration(durationVal time.Duration) string {
	durationVal = durationVal.Round(time.Second)
	hours := int(durationVal.Hours())
	minutes := int(durationVal.Minutes()) % 60
	seconds := int(durationVal.Seconds()) % 60
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
		if newMemberFile, createErr := os.Create(memberFile); createErr == nil {
			newMemberFile.Close()
		}
		members = []Member{}
		displayMembers = []Member{}
		return
	}
	defer memberCsvFile.Close()
	csvReader := csv.NewReader(memberCsvFile)
	rows, _ := csvReader.ReadAll()
	members = []Member{}
	for _, row := range rows {
		if len(row) == 2 {
			members = append(members, Member{Name: row[0], ID: row[1]})
		}
	}
	filterMembers("")
}

func appendMember(memberToAppend Member) {
	memberCsvFile, err := os.OpenFile(memberFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println("Error opening member file for append:", err)
		return
	}
	defer memberCsvFile.Close()
	csvWriter := csv.NewWriter(memberCsvFile)
	err = csvWriter.Write([]string{memberToAppend.Name, memberToAppend.ID})
	if err != nil {
		fmt.Println("Error writing member to CSV:", err)
		return
	}
	csvWriter.Flush()
	if err := csvWriter.Error(); err != nil {
		fmt.Println("Error flushing CSV writer for member:", err)
	}

	members = append(members, memberToAppend)
	if memberSearchEntry != nil {
		filterMembers(memberSearchEntry.Text)
	} else {
		filterMembers("")
	}
}

func memberByID(id string) *Member {
	for memberIndex := range members {
		if members[memberIndex].ID == id {
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
					for deviceIdx := range allDevices {
						if allDevices[deviceIdx].ID == user.PCID {
							allDevices[deviceIdx].Status = "occupied"
							allDevices[deviceIdx].UserID = user.ID
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

func getUserByID(id string) *User {
	for userIndex := range activeUsers {
		if activeUsers[userIndex].ID == id {
			return &activeUsers[userIndex]
		}
	}
	return nil
}

func getDeviceByID(id int) *Device {
	for deviceIndex := range allDevices {
		if allDevices[deviceIndex].ID == id {
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

func registerUser(name, id string, deviceID int) error {
	if _, err := strconv.Atoi(id); err != nil {
		return fmt.Errorf("user ID must be numeric")
	}
	if getUserByID(id) != nil {
		existingUser := getUserByID(id)
		return fmt.Errorf("user ID %s (%s) already checked in on Device %d", id, existingUser.Name, existingUser.PCID)
	}
	device := getDeviceByID(deviceID)
	if device == nil {
		return fmt.Errorf("device ID %d does not exist", deviceID)
	}
	if device.Status != "free" {
		return fmt.Errorf("device %d is busy (occupied by UserID: %s)", deviceID, device.UserID)
	}

	device.Status = "occupied"
	device.UserID = id

	newUser := User{ID: id, Name: name, CheckInTime: time.Now(), PCID: deviceID}
	activeUsers = append(activeUsers, newUser)

	if memberByID(id) == nil {
		appendMember(Member{Name: name, ID: id})
	}
	saveData()
	go recordLogEvent(true, newUser, deviceID, nil)
	refreshTrigger <- true
	return nil
}

func checkoutUser(userID string) error {
	if _, err := strconv.Atoi(userID); err != nil {
		return fmt.Errorf("user ID must be numeric")
	}

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
	deviceUsed := getDeviceByUserID(userID)
	loggedDeviceID := userToCheckout.PCID

	if deviceUsed != nil {
		loggedDeviceID = deviceUsed.ID
		deviceUsed.Status = "free"
		deviceUsed.UserID = ""
	} else {
		deviceFromUserStruct := getDeviceByID(userToCheckout.PCID)
		if deviceFromUserStruct != nil && deviceFromUserStruct.UserID == userID {
			deviceFromUserStruct.Status = "free"
			deviceFromUserStruct.UserID = ""
		} else {
			fmt.Printf("Warning: Could not find device for user %s (ID: %s) by UserID lookup. Device %d may need manual check.\n", userToCheckout.Name, userID, userToCheckout.PCID)
		}
	}

	activeUsers = append(activeUsers[:userIndex], activeUsers[userIndex+1:]...)

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
	if button.device.Status == "occupied" && deviceHoverDetailLabel != nil {
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
	renderer := &deviceRenderer{button: button, img: image, label: label, objects: []fyne.CanvasObject{content}}
	renderer.Refresh()
	return renderer
}

type deviceRenderer struct {
	button  *DeviceButton
	img     *canvas.Image
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
	renderer.img.File = imagePath
	renderer.img.Refresh()

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
	deviceButtons := make(map[int]*DeviceButton)
	for deviceIndex := range allDevices {
		deviceButtons[allDevices[deviceIndex].ID] = NewDeviceButton(&allDevices[deviceIndex])
	}

	paddedButton := func(deviceID int) fyne.CanvasObject {
		if button, ok := deviceButtons[deviceID]; ok {
			return container.NewPadded(button)
		}
		return widget.NewLabel(fmt.Sprintf("Error: No button for device %d", deviceID))
	}

	fixedSizePlaceholder := func(width, height float32) fyne.CanvasObject {
		rect := canvas.NewRectangle(color.Transparent)
		rect.SetMinSize(fyne.NewSize(width, height))
		return rect
	}

	pcLayout := container.NewVBox(
		container.NewHBox(paddedButton(16), paddedButton(15), paddedButton(14)), layout.NewSpacer(),
		container.NewHBox(paddedButton(11), paddedButton(12), paddedButton(13)), fixedSizePlaceholder(0, 10),
		container.NewHBox(paddedButton(10), paddedButton(9), paddedButton(8)), layout.NewSpacer(),
		container.NewHBox(paddedButton(7), paddedButton(6), paddedButton(5)), layout.NewSpacer(),
		container.NewHBox(paddedButton(1), paddedButton(2), paddedButton(3), paddedButton(4)),
	)

	consoleLayout := container.NewVBox(paddedButton(17), layout.NewSpacer(), paddedButton(18))
	leftDecorations := container.NewVBox(layout.NewSpacer())

	roomGrid := container.NewBorder(nil, nil, leftDecorations, consoleLayout, container.NewCenter(pcLayout))

	if deviceHoverDetailLabel == nil {
		deviceHoverDetailLabel = widget.NewLabel("")
		deviceHoverDetailLabel.Wrapping = fyne.TextWrapWord
		deviceHoverDetailLabel.Alignment = fyne.TextAlignCenter
	}
	return container.NewBorder(nil, deviceHoverDetailLabel, nil, nil, roomGrid)
}

func buildActiveUsersInfoTabView() fyne.CanvasObject {
	if activeUserNetworkInstance == nil {
		activeUserNetworkInstance = NewActiveUserNetworkWidget()
	}
	activeUserNetworkInstance.UpdateUsers(activeUsers)
	return activeUserNetworkInstance
}

func filterMembers(searchTextValue string) {
	searchTextValue = strings.ToLower(strings.TrimSpace(searchTextValue))
	newDisplayMembers := []Member{}
	if searchTextValue == "" {
		newDisplayMembers = make([]Member, len(members))
		copy(newDisplayMembers, members)
	} else {
		for _, member := range members {
			if strings.Contains(strings.ToLower(member.Name), searchTextValue) || strings.Contains(strings.ToLower(member.ID), searchTextValue) {
				newDisplayMembers = append(newDisplayMembers, member)
			}
		}
	}
	displayMembers = newDisplayMembers
	if memberTable != nil {
		memberTable.Refresh()
	}
}

func buildMembershipView() fyne.CanvasObject {
	memberSearchEntry = widget.NewEntry()
	memberSearchEntry.SetPlaceHolder("Search Name/ID...")
	memberSearchEntry.OnChanged = filterMembers

	memberTable = widget.NewTable(
		func() (int, int) { return len(displayMembers) + 1, 2 },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(cellID widget.TableCellID, cellObject fyne.CanvasObject) {
			label := cellObject.(*widget.Label)
			if cellID.Row == 0 {
				label.TextStyle.Bold = true
				if cellID.Col == 0 {
					label.SetText("Name")
				} else {
					label.SetText("ID")
				}
				return
			}
			label.TextStyle.Bold = false
			memberIndex := cellID.Row - 1
			if memberIndex < len(displayMembers) {
				member := displayMembers[memberIndex]
				if cellID.Col == 0 {
					label.SetText(member.Name)
				} else {
					label.SetText(member.ID)
				}
			} else {
				label.SetText("")
			}
		})
	memberTable.SetColumnWidth(0, 240)
	memberTable.SetColumnWidth(1, 160)

	if displayMembers == nil {
		filterMembers("")
	}
	return container.NewBorder(container.NewPadded(memberSearchEntry), nil, nil, nil, container.NewScroll(memberTable))
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
	idEntry.SetPlaceHolder("9 Digits Student ID")

	if deviceIDIsFixed {
		deviceEntryWidget.SetText(strconv.Itoa(deviceID))
		deviceEntryWidget.Disable()
	} else {
		deviceEntryWidget.SetPlaceHolder("Enter Device ID")
	}

	var filteredMembers []Member
	var resultsList *widget.List
	var dialogReference dialog.Dialog

	resultsList = widget.NewList(
		func() int { return len(filteredMembers) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(listItemIndex widget.ListItemID, itemCanvasObject fyne.CanvasObject) {
			if listItemIndex >= 0 && listItemIndex < len(filteredMembers) {
				itemCanvasObject.(*widget.Label).SetText(fmt.Sprintf("%s (%s)", filteredMembers[listItemIndex].Name, filteredMembers[listItemIndex].ID))
			}
		})

	scrollableResults := container.NewScroll(resultsList)
	scrollableResults.SetMinSize(fyne.NewSize(dialogWidth-40, dialogResultsListHeight-10))
	scrollableResults.Hide()

	resultsList.OnSelected = func(selectedListItemID widget.ListItemID) {
		if selectedListItemID >= 0 && selectedListItemID < len(filteredMembers) {
			selectedMember := filteredMembers[selectedListItemID]
			nameEntry.SetText(selectedMember.Name)
			idEntry.SetText(selectedMember.ID)
			searchEntry.SetText("")
			scrollableResults.Hide()
			resultsList.UnselectAll()
			filteredMembers = []Member{}
			resultsList.Refresh()
			if dialogReference != nil {
				dialogReference.Resize(fyne.NewSize(dialogWidth, dialogBaseHeight))
			}
		}
	}

	searchEntry.OnChanged = func(searchTextValue string) {
		searchTextValue = strings.ToLower(strings.TrimSpace(searchTextValue))
		if searchTextValue == "" {
			filteredMembers = []Member{}
		} else {
			newFilteredMembers := []Member{}
			for _, member := range members {
				if strings.Contains(strings.ToLower(member.Name), searchTextValue) || strings.Contains(strings.ToLower(member.ID), searchTextValue) {
					newFilteredMembers = append(newFilteredMembers, member)
				}
			}
			filteredMembers = newFilteredMembers
		}
		resultsList.Refresh()

		if dialogReference != nil {
			if len(filteredMembers) > 0 {
				scrollableResults.Show()
				dialogReference.Resize(fyne.NewSize(dialogWidth, dialogBaseHeight+dialogResultsListHeight))
			} else {
				scrollableResults.Hide()
				dialogReference.Resize(fyne.NewSize(dialogWidth, dialogBaseHeight))
			}
		}
	}

	formWidget := widget.NewForm(
		widget.NewFormItem("Name:", nameEntry),
		widget.NewFormItem("User ID:", idEntry),
		widget.NewFormItem("Device ID:", deviceEntryWidget),
	)

	onConfirmAction := func() {
		userIDString := strings.TrimSpace(idEntry.Text)
		userName := strings.TrimSpace(nameEntry.Text)

		if userName == "" || userIDString == "" {
			dialog.ShowError(fmt.Errorf("name and ID are required"), mainWindow)
			return
		}
		if _, err := strconv.Atoi(userIDString); err != nil {
			dialog.ShowError(fmt.Errorf("ID must be numeric"), mainWindow)
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

	dialogContent := container.NewVBox(searchEntry, scrollableResults, formWidget)

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
		maxNameLen := 25
		displayName := user.Name
		if len(displayName) > maxNameLen {
			displayName = displayName[:maxNameLen-3] + "..."
		}
		activeUserDisplayStrings[index] = fmt.Sprintf("%s (ID: %s, PC: %d)", displayName, user.ID, user.PCID)
		activeUserIDs[index] = user.ID
	}

	idSelector := widget.NewSelectEntry(activeUserDisplayStrings)
	idSelector.SetPlaceHolder("Select User to Check Out")

	formItems := []*widget.FormItem{
		{Text: "User:", Widget: idSelector},
	}

	d := dialog.NewForm("Check Out User", "Check Out", "Cancel", formItems, func(confirmed bool) {
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

	d.Resize(fyne.NewSize(450, d.MinSize().Height))
	d.Show()
}

func main() {
	initData()
	if err := os.MkdirAll(imgBaseDir, 0755); err != nil {
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
	activeUsersTabContent := buildActiveUsersInfoTabView()
	membershipView := buildMembershipView()
	logView := buildLogView()

	resetLayoutButton := widget.NewButton("Reset Network Layout", func() {
		if activeUserNetworkInstance != nil && tabs.Selected() != nil && tabs.Selected().Text == "Active Users" {
			activeUserNetworkInstance.pcPositions = make(map[string]fyne.Position)
			activeUserNetworkInstance.UpdateUsers(activeUsers)
		}
	})
	resetLayoutButton.Hide()

	checkInButton := widget.NewButtonWithIcon("Check In", theme.ContentAddIcon(), showCheckInDialog)
	checkOutButton := widget.NewButtonWithIcon("Check Out", theme.ContentRemoveIcon(), showCheckOutDialog)
	// Removed Refresh Data button
	toolbar := container.NewHBox(checkInButton, checkOutButton, layout.NewSpacer(), resetLayoutButton)

	totalDevicesLabel := widget.NewLabel("")
	activeUsersLabel := widget.NewLabel("") // Occupied label removed

	updateStatusLabels := func() {
		totalDevicesLabel.SetText(fmt.Sprintf("Total Devices: %d", len(allDevices)))
		activeUsersLabel.SetText(fmt.Sprintf("Active Users: %d", len(activeUsers)))
	}
	updateStatusLabels()

	statusBar := container.NewHBox(totalDevicesLabel, widget.NewLabel(" | "), activeUsersLabel)

	tabs = container.NewAppTabs(
		container.NewTabItem("Device Status", deviceStatusTabContent),
		container.NewTabItem("Active Users", activeUsersTabContent),
		container.NewTabItem("Membership", membershipView),
		container.NewTabItem("Log", logView),
	)

	for index, tabItem := range tabs.Items {
		switch tabItem.Text {
		case "Device Status":
			deviceStatusTabIndex = index
		case "Active Users":
			activeUsersTabIndex = index
		case "Membership":
			membershipTabIndex = index
		case "Log":
			logTabIndex = index
		}
	}

	tabs.SetTabLocation(container.TabLocationTop)
	tabs.OnSelected = func(selectedTabItem *container.TabItem) {
		if selectedTabItem.Text == "Log" && logTabIndex != -1 {
			resetLayoutButton.Hide()
			updateCurrentLogEntriesCache()
			if logTable != nil {
				logTable.Refresh()
			}
			logRefreshPending = false
		} else if selectedTabItem.Text == "Active Users" && activeUsersTabIndex != -1 {
			resetLayoutButton.Show()
			if activeUserNetworkInstance != nil {
				activeUserNetworkInstance.UpdateUsers(activeUsers)
			}
		} else {
			resetLayoutButton.Hide()
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
					if tabs.Selected() != nil && tabs.Selected().Text == "Active Users" {
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
					if activeUsersTabIndex != -1 && tabs != nil && len(tabs.Items) > activeUsersTabIndex {
						if tabs.Items[activeUsersTabIndex].Content != activeUserNetworkInstance {
							tabs.Items[activeUsersTabIndex].Content = activeUserNetworkInstance
						}
					}

					updateStatusLabels()

					if logRefreshPending || (tabs.Selected() != nil && tabs.Selected().Text == "Log") {
						updateCurrentLogEntriesCache()
						if logTable != nil {
							logTable.Refresh()
						}
						logRefreshPending = false
					}
					if memberSearchEntry != nil {
						filterMembers(memberSearchEntry.Text)
					} else {
						filterMembers("")
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

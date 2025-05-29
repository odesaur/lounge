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
	membershipFilteredMembers []Member
	mainWindow                fyne.Window
	logTable                  *widget.Table
	refreshTrigger            = make(chan bool, 1)
	logRefreshPending         = false
	logFileMutex              sync.Mutex
	currentLogEntries         []LogEntry
	fzfMemberSearchEntry      *widget.Entry
	tabs                      *container.AppTabs
	deviceStatusTabIndex      int = -1
	activeUsersTabIndex       int = -1
	membershipTabIndex        int = -1
	logTabIndex               int = -1
	deviceHoverDetailLabel    *widget.Label
	activeUserNetworkInstance *ActiveUserNetworkWidget
	membershipResultsList     *widget.List
	membershipDetailLabel     *widget.Label
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
	consoleBusyImage fyne.Resource
	containerSize    fyne.Size
	draggingUserID   string
	dragOffset       fyne.Position
	isDragging       bool
}

func NewActiveUserNetworkWidget() *ActiveUserNetworkWidget {
	widget := &ActiveUserNetworkWidget{
		users:       make([]User, 0),
		pcPositions: make(map[string]fyne.Position),
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	busyPCPath := filepath.Join(imgBaseDir, "busy.png")
	if _, err := os.Stat(busyPCPath); err == nil {
		widget.busyPCImage, _ = fyne.LoadResourceFromPath(busyPCPath)
	} else {
		fmt.Printf("Warning: busy.png not found at %s. Using fallback.\n", busyPCPath)
		widget.busyPCImage = theme.ComputerIcon()
	}

	consoleBusyPath := filepath.Join(imgBaseDir, "console_busy.png")
	if _, err := os.Stat(consoleBusyPath); err == nil {
		widget.consoleBusyImage, _ = fyne.LoadResourceFromPath(consoleBusyPath)
	} else {
		fmt.Printf("Warning: console_busy.png not found at %s. Using busyPCImage as fallback for consoles.\n", consoleBusyPath)
		widget.consoleBusyImage = widget.busyPCImage
	}

	widget.ExtendBaseWidget(widget)
	return widget
}

func (widget *ActiveUserNetworkWidget) UpdateUsers(users []User) {
	newUsersMap := make(map[string]User)
	for _, user := range users {
		newUsersMap[user.ID] = user
	}

	existingUserIDsInWidget := make(map[string]bool)
	for _, user := range widget.users {
		existingUserIDsInWidget[user.ID] = true
	}

	usersActuallyChanged := false
	if len(users) != len(widget.users) {
		usersActuallyChanged = true
	} else {
		for _, user := range users {
			if _, exists := existingUserIDsInWidget[user.ID]; !exists {
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

	for userID := range widget.pcPositions {
		if _, exists := newUsersMap[userID]; !exists {
			delete(widget.pcPositions, userID)
			usersActuallyChanged = true
		}
	}

	widget.users = make([]User, len(users))
	copy(widget.users, users)

	newUsersAdded := false
	for _, user := range widget.users {
		if _, exists := widget.pcPositions[user.ID]; !exists {
			newUsersAdded = true
			break
		}
	}

	if usersActuallyChanged || newUsersAdded || (len(widget.users) > 0 && len(widget.pcPositions) == 0) {
		widget.calculatePCPositions()
	}
	widget.Refresh()
}

func (widget *ActiveUserNetworkWidget) calculatePCPositions() {
	if widget.containerSize.IsZero() {
		return
	}
	if len(widget.users) == 0 {
		widget.pcPositions = make(map[string]fyne.Position)
		return
	}

	center := fyne.NewPos(widget.containerSize.Width/2, widget.containerSize.Height/2)

	usersToPlace := []User{}
	for _, user := range widget.users {
		if _, exists := widget.pcPositions[user.ID]; !exists {
			usersToPlace = append(usersToPlace, user)
		}
	}

	if len(usersToPlace) == 0 {
		return
	}

	if len(widget.users) == 1 && len(usersToPlace) == 1 {
		widget.pcPositions[usersToPlace[0].ID] = center
		return
	}

	if len(widget.users) > 0 {
		firstUserID := widget.users[0].ID
		needsPlacing := false
		for _, user := range usersToPlace {
			if user.ID == firstUserID {
				needsPlacing = true
				break
			}
		}
		if _, alreadyHasPos := widget.pcPositions[firstUserID]; !alreadyHasPos && needsPlacing {
			widget.pcPositions[firstUserID] = center
			tempUsersToPlace := []User{}
			for _, user := range usersToPlace {
				if user.ID != firstUserID {
					tempUsersToPlace = append(tempUsersToPlace, user)
				}
			}
			usersToPlace = tempUsersToPlace
		}
	}
	if len(usersToPlace) == 0 {
		return
	}

	currentRadius := initialRingRadius
	if len(widget.pcPositions) > 1 {
		maxRadius := float32(0.0)
		for _, pos := range widget.pcPositions {
			if pos.IsZero() {
				continue
			}
			distanceX := pos.X - center.X
			distanceY := pos.Y - center.Y
			radius := float32(math.Sqrt(float64(distanceX*distanceX + distanceY*distanceY)))
			if radius > maxRadius {
				maxRadius = radius
			}
		}
		if maxRadius > 0 && maxRadius+ringStep > currentRadius {
			currentRadius = maxRadius + ringStep
		}
	}

	nodesPlacedOnCurrentRing := 0
	currentRingCapacity := int(math.Max(1.0, (2*math.Pi*float64(currentRadius))/(float64(nodeVisualWidth)*0.95)))
	if currentRingCapacity == 0 {
		currentRingCapacity = 1
	}
	initialAngleOffset := widget.rng.Float64() * (math.Pi / float64(currentRingCapacity))

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
			initialAngleOffset = widget.rng.Float64() * (math.Pi / float64(currentRingCapacity))
		}

		angle := initialAngleOffset + (2*math.Pi/float64(currentRingCapacity))*float64(nodesPlacedOnCurrentRing)
		positionX := center.X + currentRadius*float32(math.Cos(angle))
		positionY := center.Y + currentRadius*float32(math.Sin(angle))

		minIconCenterX := canvasPadding + pcImageSize/2
		if positionX-pcImageSize/2-8-infoBoxWidth < canvasPadding {
			minIconCenterX = canvasPadding + infoBoxWidth + 8 + pcImageSize/2
		}
		maxIconCenterX := widget.containerSize.Width - canvasPadding - pcImageSize/2
		if positionX+pcImageSize/2+8+infoBoxWidth > widget.containerSize.Width-canvasPadding {
			maxIconCenterX = widget.containerSize.Width - canvasPadding - infoBoxWidth - 8 - pcImageSize/2
		}
		positionX = float32(math.Max(float64(minIconCenterX), math.Min(float64(positionX), float64(maxIconCenterX))))

		minIconCenterY := canvasPadding + infoBoxHeight/2
		maxIconCenterY := widget.containerSize.Height - canvasPadding - infoBoxHeight/2
		positionY = float32(math.Max(float64(minIconCenterY), math.Min(float64(positionY), float64(maxIconCenterY))))

		finalPosition := fyne.NewPos(positionX, positionY)

		attempts := 0
		maxAttemptsToAdjust := 10
		for attempts < maxAttemptsToAdjust {
			isOverlapping := false
			for _, existingPosition := range widget.pcPositions {
				distance := math.Sqrt(math.Pow(float64(existingPosition.X-finalPosition.X), 2) + math.Pow(float64(existingPosition.Y-finalPosition.Y), 2))
				if distance < float64(nodeVisualWidth*0.70) {
					isOverlapping = true
					break
				}
			}
			if !isOverlapping {
				for _, newlyPlacedPosition := range tempNewPositions {
					distance := math.Sqrt(math.Pow(float64(newlyPlacedPosition.X-finalPosition.X), 2) + math.Pow(float64(newlyPlacedPosition.Y-finalPosition.Y), 2))
					if distance < float64(nodeVisualWidth*0.70) {
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
			positionX = center.X + currentRadius*float32(math.Cos(angle))
			positionY = center.Y + currentRadius*float32(math.Sin(angle))

			positionX = float32(math.Max(float64(minIconCenterX), math.Min(float64(positionX), float64(maxIconCenterX))))
			positionY = float32(math.Max(float64(minIconCenterY), math.Min(float64(positionY), float64(maxIconCenterY))))
			finalPosition = fyne.NewPos(positionX, positionY)
			attempts++
		}

		widget.pcPositions[user.ID] = finalPosition
		tempNewPositions[user.ID] = finalPosition
		nodesPlacedOnCurrentRing++
	}
}

func (widget *ActiveUserNetworkWidget) Dragged(event *fyne.DragEvent) {
	if !widget.isDragging {
		for _, user := range widget.users {
			pcPosition, ok := widget.pcPositions[user.ID]
			if !ok {
				continue
			}
			iconTopLeftX := pcPosition.X - pcImageSize/2
			iconTopLeftY := pcPosition.Y - pcImageSize/2

			if event.Position.X >= iconTopLeftX && event.Position.X <= iconTopLeftX+pcImageSize &&
				event.Position.Y >= iconTopLeftY && event.Position.Y <= iconTopLeftY+pcImageSize {

				widget.isDragging = true
				widget.draggingUserID = user.ID
				widget.dragOffset = fyne.NewPos(event.Position.X-pcPosition.X, event.Position.Y-pcPosition.Y)
				break
			}
		}
	}

	if widget.isDragging && widget.draggingUserID != "" {
		newPcPositionX := event.Position.X - widget.dragOffset.X
		newPcPositionY := event.Position.Y - widget.dragOffset.Y

		newPcPositionX = float32(math.Max(float64(canvasPadding+pcImageSize/2), math.Min(float64(newPcPositionX), float64(widget.containerSize.Width-canvasPadding-pcImageSize/2))))
		newPcPositionY = float32(math.Max(float64(canvasPadding+pcImageSize/2), math.Min(float64(newPcPositionY), float64(widget.containerSize.Height-canvasPadding-pcImageSize/2))))

		widget.pcPositions[widget.draggingUserID] = fyne.NewPos(newPcPositionX, newPcPositionY)
		widget.Refresh()
	}
}

func (widget *ActiveUserNetworkWidget) DragEnd() {
	widget.isDragging = false
	widget.draggingUserID = ""
}

type activeUserNetworkRenderer struct {
	widget  *ActiveUserNetworkWidget
	objects []fyne.CanvasObject
}

func (renderer *activeUserNetworkRenderer) Layout(size fyne.Size) {
	if renderer.widget.containerSize != size {
		oldSize := renderer.widget.containerSize
		renderer.widget.containerSize = size
		if oldSize.IsZero() || math.Abs(float64(oldSize.Width-size.Width)) > 50 || math.Abs(float64(oldSize.Height-size.Height)) > 50 {
			renderer.widget.calculatePCPositions()
		}
	}
	renderer.widget.Refresh()
}

func (renderer *activeUserNetworkRenderer) MinSize() fyne.Size {
	return fyne.NewSize(600, 400)
}

func (renderer *activeUserNetworkRenderer) Refresh() {
	renderer.objects = []fyne.CanvasObject{}

	if renderer.widget.busyPCImage == nil {
		renderer.objects = append(renderer.objects, widget.NewLabel("Error: busy.png resource not loaded"))
		canvas.Refresh(renderer.widget)
		return
	}

	if len(renderer.widget.users) == 0 {
		noUsersLabel := widget.NewLabel("No active users to display.")
		if !renderer.widget.containerSize.IsZero() {
			labelSize := noUsersLabel.MinSize()
			noUsersLabel.Move(fyne.NewPos(
				(renderer.widget.containerSize.Width-labelSize.Width)/2,
				(renderer.widget.containerSize.Height-labelSize.Height)/2,
			))
		}
		renderer.objects = append(renderer.objects, noUsersLabel)
		canvas.Refresh(renderer.widget)
		return
	}

	var centerUserPosition fyne.Position
	centerUserExists := false
	if len(renderer.widget.users) > 0 {
		firstUserID := renderer.widget.users[0].ID
		if position, ok := renderer.widget.pcPositions[firstUserID]; ok {
			centerUserPosition = position
			centerUserExists = true
		}
	}

	for index, user := range renderer.widget.users {
		if index == 0 && centerUserExists {
			continue
		}
		pcPosition, ok := renderer.widget.pcPositions[user.ID]
		if !ok {
			continue
		}
		if centerUserExists {
			line := canvas.NewLine(theme.PrimaryColor())
			line.Position1 = pcPosition
			line.Position2 = centerUserPosition
			line.StrokeWidth = 1.5
			renderer.objects = append(renderer.objects, line)
		}
	}

	for _, user := range renderer.widget.users {
		pcPosition, ok := renderer.widget.pcPositions[user.ID]
		if !ok {
			continue
		}

		var currentBusyImage fyne.Resource = renderer.widget.busyPCImage
		device := getDeviceByID(user.PCID)
		if device != nil && device.Type == "Console" {
			if renderer.widget.consoleBusyImage != nil {
				currentBusyImage = renderer.widget.consoleBusyImage
			}
		}

		pcIcon := canvas.NewImageFromResource(currentBusyImage)
		pcIcon.Resize(fyne.NewSize(pcImageSize, pcImageSize))
		pcIcon.Move(fyne.NewPos(pcPosition.X-pcImageSize/2, pcPosition.Y-pcImageSize/2))
		renderer.objects = append(renderer.objects, pcIcon)

		pcNumberText := canvas.NewText(strconv.Itoa(user.PCID), color.White)
		pcNumberText.TextSize = 12
		pcNumberText.Alignment = fyne.TextAlignCenter
		pcNumberText.TextStyle.Bold = true
		pcNumberText.Move(fyne.NewPos(pcPosition.X-pcNumberText.MinSize().Width/2, pcPosition.Y-pcNumberText.MinSize().Height/2))
		renderer.objects = append(renderer.objects, pcNumberText)

		infoBoxRect := canvas.NewRectangle(theme.BackgroundColor())
		infoBoxRect.SetMinSize(fyne.NewSize(infoBoxWidth, infoBoxHeight))

		infoBoxX := pcPosition.X + pcImageSize/2 + 8
		infoBoxY := pcPosition.Y - infoBoxHeight/2

		if !renderer.widget.containerSize.IsZero() {
			if infoBoxX+infoBoxWidth > renderer.widget.containerSize.Width-canvasPadding {
				infoBoxX = pcPosition.X - pcImageSize/2 - infoBoxWidth - 8
			}
			if infoBoxX < canvasPadding {
				infoBoxX = canvasPadding
			}
			if infoBoxY < canvasPadding {
				infoBoxY = canvasPadding
			}
			if infoBoxY+infoBoxHeight > renderer.widget.containerSize.Height-canvasPadding {
				infoBoxY = renderer.widget.containerSize.Height - infoBoxHeight - canvasPadding
			}
		}

		infoBoxRect.Move(fyne.NewPos(infoBoxX, infoBoxY))
		infoBoxRect.StrokeColor = theme.PrimaryColor()
		infoBoxRect.StrokeWidth = 1
		renderer.objects = append(renderer.objects, infoBoxRect)

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
		for textIndex, text := range texts {
			text.TextSize = 10
			if textIndex == 1 {
				text.TextSize = 11
				text.TextStyle.Bold = true
			}
			text.Move(fyne.NewPos(infoBoxX+5, currentY))
			renderer.objects = append(renderer.objects, text)
			currentY += text.MinSize().Height + 1
		}
	}
	canvas.Refresh(renderer.widget)
}

func (renderer *activeUserNetworkRenderer) Objects() []fyne.CanvasObject {
	return renderer.objects
}
func (renderer *activeUserNetworkRenderer) Destroy() {}

func (widget *ActiveUserNetworkWidget) CreateRenderer() fyne.WidgetRenderer {
	renderer := &activeUserNetworkRenderer{
		widget:  widget,
		objects: []fyne.CanvasObject{},
	}
	return renderer
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

// Load members from CSV file, reading from 3rd and 4th columns
func loadMembers() {
	memberCsvFile, err := os.Open(memberFile)
	if err != nil {
		if newMemberFile, createErr := os.Create(memberFile); createErr == nil {
			newMemberFile.Close()
		}
		members = []Member{}
		membershipFilteredMembers = []Member{}
		return
	}
	defer memberCsvFile.Close()
	csvReader := csv.NewReader(memberCsvFile)
	rows, _ := csvReader.ReadAll()
	members = []Member{}
	for _, row := range rows {
		if len(row) >= 4 {
			members = append(members, Member{Name: row[2], ID: row[3]})
		}
	}
	membershipFilteredMembers = []Member{}
}

func getNextMemberID() string {
	return strconv.Itoa(len(members) + 1)
}

// Append new member to CSV file
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

	// Write existing rows
	for _, row := range existingRows {
		if err := csvWriter.Write(row); err != nil {
			fmt.Println("Error writing existing row to CSV:", err)
			return
		}
	}

	// Create new row with empty first two columns
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
	if fzfMemberSearchEntry != nil && membershipResultsList != nil {
		filterFzfMembers(fzfMemberSearchEntry.Text)
	}
}

func memberByID(memberID string) *Member {
	for memberIndex := range members {
		if members[memberIndex].ID == memberID {
			return &members[memberIndex]
		}
	}
	return nil
}

func filterFzfMembers(searchTextValue string) {
	searchTextValue = strings.ToLower(strings.TrimSpace(searchTextValue))
	newFilteredMembers := []Member{}
	if searchTextValue != "" {
		for _, member := range members {
			if strings.Contains(strings.ToLower(member.Name), searchTextValue) || strings.Contains(strings.ToLower(member.ID), searchTextValue) {
				newFilteredMembers = append(newFilteredMembers, member)
			}
		}
	}
	membershipFilteredMembers = newFilteredMembers
	if membershipResultsList != nil {
		membershipResultsList.Refresh()
		if len(membershipFilteredMembers) == 0 && searchTextValue == "" {
			membershipResultsList.Hide()
			if membershipDetailLabel != nil {
				membershipDetailLabel.SetText("")
			}
		} else {
			membershipResultsList.Show()
		}
	}
}

func buildMembershipView() fyne.CanvasObject {
	fzfMemberSearchEntry = widget.NewEntry()
	fzfMemberSearchEntry.SetPlaceHolder("Search Members (Name/ID)...")

	membershipFilteredMembers = []Member{}

	membershipDetailLabel = widget.NewLabel("Select a member from the list to see details.")
	membershipDetailLabel.Wrapping = fyne.TextWrapWord
	membershipDetailLabel.Alignment = fyne.TextAlignCenter

	membershipResultsList = widget.NewList(
		func() int { return len(membershipFilteredMembers) },
		func() fyne.CanvasObject {
			return container.NewBorder(
				nil, nil, widget.NewLabel("Name Holder"), widget.NewLabel("ID Holder"),
			)
		},
		func(itemID widget.ListItemID, itemCanvasObject fyne.CanvasObject) {
			if itemID < len(membershipFilteredMembers) {
				member := membershipFilteredMembers[itemID]
				containerObject := itemCanvasObject.(*fyne.Container)
				nameLabel := containerObject.Objects[0].(*widget.Label)
				idLabel := containerObject.Objects[1].(*widget.Label)
				nameLabel.SetText(member.Name)
				idLabel.SetText(member.ID)
			}
		},
	)

	membershipResultsList.OnSelected = func(selectedID widget.ListItemID) {
		if selectedID < len(membershipFilteredMembers) {
			selectedMember := membershipFilteredMembers[selectedID]
			membershipDetailLabel.SetText(fmt.Sprintf("Selected Member:\nName: %s\nID: %s", selectedMember.Name, selectedMember.ID))
		}
	}
	membershipResultsList.Hide()

	fzfMemberSearchEntry.OnChanged = func(text string) {
		filterFzfMembers(text)
		if text == "" {
			membershipDetailLabel.SetText("Select a member from the list to see details.")
		}
	}

	scrollableResults := container.NewScroll(membershipResultsList)
	scrollableResults.SetMinSize(fyne.NewSize(300, 200))

	topContent := container.NewVBox(
		fzfMemberSearchEntry,
		scrollableResults,
	)

	return container.NewBorder(topContent, membershipDetailLabel, nil, nil)
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
							allDevices[deviceIndex].UserID = user.ID
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

func registerUser(name, userID string, deviceID int) error {
	if _, err := strconv.Atoi(userID); err != nil {
		return fmt.Errorf("user ID must be numeric")
	}
	if getUserByID(userID) != nil {
		existingUser := getUserByID(userID)
		return fmt.Errorf("user ID %s (%s) already checked in on Device %d", userID, existingUser.Name, existingUser.PCID)
	}
	device := getDeviceByID(deviceID)
	if device == nil {
		return fmt.Errorf("device ID %d does not exist", deviceID)
	}
	if device.Status != "free" {
		return fmt.Errorf("device %d is busy (occupied by UserID: %s)", deviceID, device.UserID)
	}

	device.Status = "occupied"
	device.UserID = userID

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

	noIDButton := widget.NewButton("No ID?", func() {
		nextID := getNextMemberID()
		idEntry.SetText(nextID)
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
		resetLayoutButton.Hide()

		if selectedTabItem.Text == "Log" && logTabIndex != -1 {
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
		} else if selectedTabItem.Text == "Membership" && membershipTabIndex != -1 {
			if fzfMemberSearchEntry != nil {
				fzfMemberSearchEntry.SetText("")
			}
			filterFzfMembers("")
		}
	}

	topBarLayout := container.NewVBox(toolbar, widget.NewSeparator())
	bottomBarLayout := container.NewVBox(widget.NewSeparator(), statusBar)
	mainApplicationContent := container.NewBorder(topBarLayout, bottomBarLayout, nil, nil, tabs)

	mainWindow.SetContent(mainApplicationContent)

	// Update UI periodically and check for date changes
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

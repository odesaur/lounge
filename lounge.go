package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"math"
	"math/rand" // For random placement
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

	pcImageSize    float32 = 48 // Size of the PC icon
	infoBoxWidth   float32 = 180
	infoBoxHeight  float32 = 90  // Adjusted to better fit 5 lines of text
	nodeSeparation float32 = 100 // Average separation between nodes
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

// --- ClassicTheme Definition (Moved here for visibility) ---
type classicTheme struct{ fyne.Theme }

func newClassicTheme() fyne.Theme { return &classicTheme{Theme: theme.LightTheme()} }

func (themeInstance *classicTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	classicBackground := color.NRGBA{R: 0xC0, G: 0xC0, B: 0xC0, A: 0xFF}
	classicButtonFace := color.NRGBA{R: 0xD4, G: 0xD0, B: 0xC8, A: 0xFF}
	classicText := color.Black
	classicPrimary := color.NRGBA{R: 0x00, G: 0x00, B: 0x80, A: 0xFF} // Navy Blue
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
		return color.NRGBA{R: 0xB0, G: 0xB0, B: 0xD0, A: 0xFF} // Lighter blue/purple for hover
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
	case theme.ColorNameForeground: // For text
		return classicText
	case theme.ColorNameSeparator:
		return classicShadow
	default:
		// Fallback to the embedded theme's colors
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
		return 12 // Default text size
	case theme.SizeNameInputBorder:
		return 1
	default:
		return themeInstance.Theme.Size(name)
	}
}

// --- ActiveUserNetworkWidget ---
type ActiveUserNetworkWidget struct {
	widget.BaseWidget
	users         []User
	pcPositions   map[string]fyne.Position
	rng           *rand.Rand
	busyPCImage   fyne.Resource
	containerSize fyne.Size
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

	w.ExtendBaseWidget(w)
	// activeUserNetworkInstance = w // Assign to global if needed for external direct manipulation
	return w
}

func (w *ActiveUserNetworkWidget) UpdateUsers(users []User) {
	usersChanged := len(w.users) != len(users)
	if !usersChanged {
		for i := range users {
			found := false
			for j := range w.users {
				if users[i].ID == w.users[j].ID && users[i].CheckInTime.Equal(w.users[j].CheckInTime) {
					found = true
					break
				}
			}
			if !found {
				usersChanged = true
				break
			}
		}
	}

	w.users = make([]User, len(users))
	copy(w.users, users)

	if usersChanged || len(w.pcPositions) != len(w.users) { // Recalculate if list changed or positions are mismatched
		w.calculatePCPositions()
	}
	w.Refresh()
}

func (w *ActiveUserNetworkWidget) calculatePCPositions() {
	if w.containerSize.IsZero() {
		return
	}
	newPositions := make(map[string]fyne.Position)
	center := fyne.NewPos(w.containerSize.Width/2, w.containerSize.Height/2)
	takenPositions := []fyne.Position{}

	for i, user := range w.users {
		var pos fyne.Position
		attempts := 0
		const maxAttempts = 30

		for attempts < maxAttempts {
			if len(w.users) == 1 { // Single user directly in center
				pos = center
			} else if i == 0 && len(w.users) > 1 { // First of multiple, slightly offset from center
				angle := w.rng.Float64() * 2 * math.Pi
				radius := nodeSeparation * 0.5 // Start closer for first few
				pos = center.Add(fyne.NewPos(radius*float32(math.Cos(angle)), radius*float32(math.Sin(angle))))
			} else {
				angle := w.rng.Float64() * 2 * math.Pi
				radius := nodeSeparation + w.rng.Float32()*nodeSeparation*0.3
				if len(w.users) > 5 {
					radius *= (1 + float32(len(w.users)-5)*0.03)
				}
				basePos := center
				if len(takenPositions) > 0 {
					// Bias towards connecting to the previous node in the list for a chain-like effect
					// or pick a random one for a more spread-out graph
					if i > 0 {
						prevUser := w.users[i-1]
						if p, ok := newPositions[prevUser.ID]; ok { // Use newPositions as it's being built
							basePos = p
						} else if len(takenPositions) > 0 { // Fallback
							basePos = takenPositions[w.rng.Intn(len(takenPositions))]
						}
					} else {
						basePos = takenPositions[w.rng.Intn(len(takenPositions))]
					}
				}

				offsetX := radius * float32(math.Cos(angle))
				offsetY := radius * float32(math.Sin(angle))
				pos = basePos.Add(fyne.NewPos(offsetX, offsetY))
			}

			pos.X = float32(math.Max(float64(pcImageSize/2+5), math.Min(float64(pos.X), float64(w.containerSize.Width-pcImageSize/2-infoBoxWidth-5))))
			pos.Y = float32(math.Max(float64(pcImageSize/2+infoBoxHeight/2+5), math.Min(float64(pos.Y), float64(w.containerSize.Height-infoBoxHeight/2-5))))

			tooClose := false
			for _, p := range takenPositions {
				dist := math.Sqrt(math.Pow(float64(p.X-pos.X), 2) + math.Pow(float64(p.Y-pos.Y), 2))
				if dist < float64(pcImageSize+10) { // A bit more spacing than just icon size
					tooClose = true
					break
				}
			}
			if !tooClose {
				break
			}
			attempts++
		}
		newPositions[user.ID] = pos
		takenPositions = append(takenPositions, pos)
	}
	w.pcPositions = newPositions
}

func (w *ActiveUserNetworkWidget) CreateRenderer() fyne.WidgetRenderer {
	r := &activeUserNetworkRenderer{
		widget:  w,
		objects: []fyne.CanvasObject{},
	}
	return r
}

type activeUserNetworkRenderer struct {
	widget  *ActiveUserNetworkWidget
	objects []fyne.CanvasObject
}

func (r *activeUserNetworkRenderer) Layout(size fyne.Size) {
	if r.widget.containerSize != size { // Recalculate only if size actually changes
		r.widget.containerSize = size
		r.widget.calculatePCPositions()
	}
}

func (r *activeUserNetworkRenderer) MinSize() fyne.Size {
	return fyne.NewSize(600, 400)
}

func (r *activeUserNetworkRenderer) Refresh() {
	r.objects = []fyne.CanvasObject{}

	if r.widget.busyPCImage == nil {
		r.objects = append(r.objects, widget.NewLabel("Error: busy.png not loaded"))
		canvas.Refresh(r.widget)
		return
	}
	if len(r.widget.users) == 0 {
		// Center the "No active users" message
		noUsersLabel := widget.NewLabel("No active users to display.")
		if !r.widget.containerSize.IsZero() {
			noUsersLabel.Move(fyne.NewPos(
				(r.widget.containerSize.Width-noUsersLabel.MinSize().Width)/2,
				(r.widget.containerSize.Height-noUsersLabel.MinSize().Height)/2,
			))
		}
		r.objects = append(r.objects, noUsersLabel)
		canvas.Refresh(r.widget)
		return
	}

	// Draw lines first so they are behind icons/boxes
	for i, user := range r.widget.users {
		if i == 0 {
			continue
		} // No line for the first user

		pcPos, ok := r.widget.pcPositions[user.ID]
		if !ok {
			continue
		}

		// Connect to the actual previous user in the list for a somewhat ordered network
		prevUser := r.widget.users[i-1]
		prevPos, prevOk := r.widget.pcPositions[prevUser.ID]
		if prevOk {
			line := canvas.NewLine(theme.PrimaryColor())
			line.Position1 = pcPos
			line.Position2 = prevPos
			line.StrokeWidth = 1.5 // Thinner line
			r.objects = append(r.objects, line)
		}
	}

	for _, user := range r.widget.users { // Iterate again to draw nodes on top of lines
		pcPos, ok := r.widget.pcPositions[user.ID]
		if !ok {
			fmt.Println("Warning: Position not found for user", user.ID, "during refresh.")
			// Attempt to recalculate if missing, though ideally Layout should handle this
			if !r.widget.containerSize.IsZero() {
				r.widget.calculatePCPositions()
				pcPos = r.widget.pcPositions[user.ID] // Try to get it again
				if pcPos.IsZero() {                   // Still zero after recalc means something is off
					pcPos = fyne.NewPos(r.widget.containerSize.Width/2, r.widget.containerSize.Height/2)
				}
			} else {
				continue // Skip if size is zero and cannot calculate
			}
		}

		pcIcon := canvas.NewImageFromResource(r.widget.busyPCImage)
		pcIcon.Resize(fyne.NewSize(pcImageSize, pcImageSize))
		pcIcon.Move(fyne.NewPos(pcPos.X-pcImageSize/2, pcPos.Y-pcImageSize/2))
		r.objects = append(r.objects, pcIcon)

		pcNumText := canvas.NewText(strconv.Itoa(user.PCID), color.White) // White for contrast on icon
		pcNumText.TextSize = 12
		pcNumText.Alignment = fyne.TextAlignCenter
		pcNumText.TextStyle.Bold = true
		// Center on icon
		pcNumText.Move(fyne.NewPos(pcPos.X-pcNumText.MinSize().Width/2, pcPos.Y-pcNumText.MinSize().Height/2))
		r.objects = append(r.objects, pcNumText)

		infoBoxRect := canvas.NewRectangle(theme.BackgroundColor())
		infoBoxRect.SetMinSize(fyne.NewSize(infoBoxWidth, infoBoxHeight))
		infoBoxX := pcPos.X + pcImageSize/2 + 8
		infoBoxY := pcPos.Y - infoBoxHeight/2

		if infoBoxX+infoBoxWidth > r.widget.containerSize.Width-5 { // -5 for margin
			infoBoxX = pcPos.X - pcImageSize/2 - infoBoxWidth - 8
		}
		if infoBoxY < 5 {
			infoBoxY = 5
		}
		if infoBoxY+infoBoxHeight > r.widget.containerSize.Height-5 {
			infoBoxY = r.widget.containerSize.Height - infoBoxHeight - 5
		}

		infoBoxRect.Move(fyne.NewPos(infoBoxX, infoBoxY))
		infoBoxRect.StrokeColor = theme.PrimaryColor()
		infoBoxRect.StrokeWidth = 1
		r.objects = append(r.objects, infoBoxRect)

		line1 := fmt.Sprintf("PC: %d", user.PCID)
		line2 := fmt.Sprintf("%s", user.Name) // Name only for brevity
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

		currentY := infoBoxY + 3 // Start Y for text inside box
		for textIdx, txt := range texts {
			txt.TextSize = 10
			if textIdx == 1 { // Name slightly larger
				txt.TextSize = 11
				txt.TextStyle.Bold = true
			}
			txt.Move(fyne.NewPos(infoBoxX+5, currentY))
			r.objects = append(r.objects, txt)
			currentY += txt.MinSize().Height + 1 // Tighter spacing
		}
	}
	canvas.Refresh(r.widget)
}

func (r *activeUserNetworkRenderer) Objects() []fyne.CanvasObject {
	return r.objects
}
func (r *activeUserNetworkRenderer) Destroy() {}

// --- Other functions (ensureLogDir, log functions, member functions, etc.) ---
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
		activeUserDisplayStrings[index] = fmt.Sprintf("%s (ID: %s, Device: %d)", user.Name, user.ID, user.PCID)
		activeUserIDs[index] = user.ID
	}

	idSelector := widget.NewSelectEntry(activeUserDisplayStrings)
	idSelector.SetPlaceHolder("Select User to Check Out")

	formItems := []*widget.FormItem{
		{Text: "User:", Widget: idSelector},
	}

	dialog.ShowForm("Check Out User", "Check Out", "Cancel", formItems, func(confirmed bool) {
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
			dialog.ShowError(fmt.Errorf("invalid user selection or typed entry"), mainWindow)
			return
		}

		if err := checkoutUser(userIDToCheckout); err != nil {
			dialog.ShowError(err, mainWindow)
		}
	}, mainWindow)
}

func main() {
	initData()
	if err := os.MkdirAll(imgBaseDir, 0755); err != nil {
		fmt.Printf("Warning: Unable to create image directory '%s': %v. Images might not load.\n", imgBaseDir, err)
	}

	loungeApp := app.New()
	loungeApp.Settings().SetTheme(newClassicTheme()) // Corrected: Theme applied
	mainWindow = loungeApp.NewWindow("Lounge Management System")
	mainWindow.Resize(fyne.NewSize(1080, 720))

	deviceHoverDetailLabel = widget.NewLabel("")
	deviceHoverDetailLabel.Wrapping = fyne.TextWrapWord
	deviceHoverDetailLabel.Alignment = fyne.TextAlignCenter

	activeUserNetworkInstance = NewActiveUserNetworkWidget() // Initialize global instance

	deviceStatusTabContent := buildDeviceRoomContent()
	activeUsersTabContent := buildActiveUsersInfoTabView() // This now uses the network widget
	membershipView := buildMembershipView()
	logView := buildLogView()

	checkInButton := widget.NewButtonWithIcon("Check In", theme.ContentAddIcon(), showCheckInDialog)
	checkOutButton := widget.NewButtonWithIcon("Check Out", theme.ContentRemoveIcon(), showCheckOutDialog)
	refreshButton := widget.NewButtonWithIcon("Refresh Data", theme.ViewRefreshIcon(), func() {
		initData()
		refreshTrigger <- true
	})
	toolbar := container.NewHBox(checkInButton, checkOutButton, layout.NewSpacer(), refreshButton)

	totalDevicesLabel := widget.NewLabel("")
	occupiedDevicesLabel := widget.NewLabel("")
	activeUsersLabel := widget.NewLabel("")

	updateStatusLabels := func() {
		occupiedCount := 0
		for _, device := range allDevices {
			if device.Status == "occupied" {
				occupiedCount++
			}
		}
		totalDevicesLabel.SetText(fmt.Sprintf("Total Devices: %d", len(allDevices)))
		occupiedDevicesLabel.SetText(fmt.Sprintf("Occupied: %d", occupiedCount))
		activeUsersLabel.SetText(fmt.Sprintf("Active Users: %d", len(activeUsers)))
	}
	updateStatusLabels()

	statusBar := container.NewHBox(totalDevicesLabel, widget.NewLabel(" | "), occupiedDevicesLabel, widget.NewLabel(" | "), activeUsersLabel)

	tabs = container.NewAppTabs(
		container.NewTabItem("Device Status", deviceStatusTabContent),
		container.NewTabItem("Active Users", activeUsersTabContent), // Use the built content
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
			updateCurrentLogEntriesCache()
			if logTable != nil {
				logTable.Refresh()
			}
			logRefreshPending = false
		} else if selectedTabItem.Text == "Active Users" && activeUsersTabIndex != -1 {
			if activeUserNetworkInstance != nil {
				activeUserNetworkInstance.UpdateUsers(activeUsers) // Ensure it's up-to-date on tab select
			}
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
							activeUserNetworkInstance.UpdateUsers(activeUsers)
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
					// Ensure the tab content is correctly set if it was rebuilt
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

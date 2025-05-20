package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
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

// File paths and directories
const (
	userDataFile = "log/active_users.json"
	memberFile   = "membership.csv"
	logDir       = "log"
	imgBaseDir   = "src"
)

// Data structures
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

// Global variables
var (
	allDevices           []Device
	activeUsers          []User
	members              []Member
	displayMembers       []Member
	mainWindow           fyne.Window
	deviceRoom           *fyne.Container
	activeUserList       *widget.List
	memberTable          *widget.Table
	logTable             *widget.Table
	refreshTrigger       = make(chan bool, 1)
	logRefreshPending    = false
	logFileMutex         sync.Mutex
	currentLogEntries    []LogEntry
	memberSearchEntry    *widget.Entry
	tabs                 *container.AppTabs
	deviceStatusTabIndex int = -1
	activeUsersTabIndex  int = -1
	membershipTabIndex   int = -1
	logTabIndex          int = -1
)

// Windows Classic Theme implementation
type classicTheme struct{ fyne.Theme }

func newClassicTheme() fyne.Theme {
	return &classicTheme{Theme: theme.LightTheme()}
}

// Customize colors for Windows Classic look
func (themeInstance *classicTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	// Set Windows classic theme colors
	if name == theme.ColorNameSelection {
		return color.NRGBA{R: 0xC0, G: 0xC0, B: 0xE0, A: 0xFF}
	}
	if name == theme.ColorNameHover {
		return color.NRGBA{R: 0xE0, G: 0xE0, B: 0xF0, A: 0xFF}
	}
	if name == theme.ColorNameBackground {
		return color.NRGBA{R: 0xF0, G: 0xF0, B: 0xF0, A: 0xFF}
	}
	if name == theme.ColorNameButton {
		return color.NRGBA{R: 0xD4, G: 0xD0, B: 0xC8, A: 0xFF}
	}
	if name == theme.ColorNamePrimary {
		return color.NRGBA{R: 0x00, G: 0x00, B: 0x80, A: 0xFF}
	}
	return themeInstance.Theme.Color(name, variant)
}

func (themeInstance *classicTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return themeInstance.Theme.Icon(name)
}

func (themeInstance *classicTheme) Font(style fyne.TextStyle) fyne.Resource {
	return themeInstance.Theme.Font(style)
}

func (themeInstance *classicTheme) Size(name fyne.ThemeSizeName) float32 {
	// Adjust sizes for Windows classic look
	if name == theme.SizeNamePadding {
		return 6
	}
	if name == theme.SizeNameInlineIcon {
		return 24
	}
	return themeInstance.Theme.Size(name)
}

// Log file functions
func ensureLogDir() error {
	return os.MkdirAll(logDir, 0755)
}

func getLogFilePath() string {
	today := time.Now().Format("2006-01-02")
	return filepath.Join(logDir, fmt.Sprintf("lounge-%s.json", today))
}

func readDailyLogEntries() ([]LogEntry, error) {
	filePath := getLogFilePath()
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return []LogEntry{}, nil
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("error opening log: %s: %w", filePath, err)
	}
	defer file.Close()

	byteValue, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("error reading log: %s: %w", filePath, err)
	}

	var entries []LogEntry
	if len(byteValue) > 0 {
		if err := json.Unmarshal(byteValue, &entries); err != nil {
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

// Record log events for check-in and check-out
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
		// Add check-in entry
		entries = append(entries, LogEntry{
			UserName:    user.Name,
			UserID:      user.ID,
			PCID:        deviceID,
			CheckInTime: user.CheckInTime,
		})
	} else {
		// Update existing entry with check-out information
		found := false
		for i := len(entries) - 1; i >= 0; i-- {
			entry := entries[i]
			if entry.UserID == user.ID && entry.PCID == deviceID && entry.CheckOutTime.IsZero() {
				matchTime := originalCheckInTimeForUpdate == nil ||
					(originalCheckInTimeForUpdate != nil && entry.CheckInTime.Equal(*originalCheckInTimeForUpdate))
				if matchTime {
					entries[i].CheckOutTime = time.Now()
					entries[i].UsageTime = formatDuration(entries[i].CheckOutTime.Sub(entries[i].CheckInTime))
					found = true
					break
				}
			}
		}
		if !found {
			fmt.Printf("Error: No matching check-in for user %s (ID: %s) Device %d.\n",
				user.Name, user.ID, deviceID)
		}
	}

	// Write updated entries back to file
	if err := writeDailyLogEntries(entries); err != nil {
		fmt.Println("Error writing daily log:", err)
	}

	// Update UI
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

// Format time duration to readable string
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

// Update log entries cache
func updateCurrentLogEntriesCache() {
	entries, err := readDailyLogEntries()
	if err != nil {
		fmt.Println("Error updating log cache:", err)
		currentLogEntries = []LogEntry{}
	} else {
		currentLogEntries = entries
	}
}

// Build log view with table
func buildLogView() fyne.CanvasObject {
	updateCurrentLogEntriesCache()

	// Create table for log entries
	logTable = widget.NewTable(
		func() (int, int) {
			return len(currentLogEntries) + 1, 6
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("")
		},
		func(id widget.TableCellID, cell fyne.CanvasObject) {
			label := cell.(*widget.Label)

			// Header row
			if id.Row == 0 {
				label.TextStyle.Bold = true
				switch id.Col {
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

			// Data rows
			label.TextStyle.Bold = false
			entryIndex := id.Row - 1
			if entryIndex < len(currentLogEntries) {
				entry := currentLogEntries[entryIndex]
				switch id.Col {
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

	// Set column widths for better readability
	logTable.SetColumnWidth(0, 180)
	logTable.SetColumnWidth(1, 100)
	logTable.SetColumnWidth(2, 70)
	logTable.SetColumnWidth(3, 150)
	logTable.SetColumnWidth(4, 150)
	logTable.SetColumnWidth(5, 120)

	return container.NewScroll(logTable)
}

// Load member data from CSV file
func loadMembers() {
	f, err := os.Open(memberFile)
	if err != nil {
		// Create empty file if it doesn't exist
		if file, createErr := os.Create(memberFile); createErr == nil {
			file.Close()
		}
		members = []Member{}
		displayMembers = []Member{}
		return
	}
	defer f.Close()

	// Read CSV content
	r := csv.NewReader(f)
	rows, _ := r.ReadAll()
	members = []Member{}

	for _, row := range rows {
		if len(row) == 2 {
			members = append(members, Member{Name: row[0], ID: row[1]})
		}
	}

	filterMembers("")
}

// Add a new member to the CSV file
func appendMember(m Member) {
	f, _ := os.OpenFile(memberFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	defer f.Close()

	w := csv.NewWriter(f)
	w.Write([]string{m.Name, m.ID})
	w.Flush()

	members = append(members, m)
	if memberSearchEntry != nil {
		filterMembers(memberSearchEntry.Text)
	} else {
		filterMembers("")
	}
}

// Find member by ID
func memberByID(id string) *Member {
	for i := range members {
		if members[i].ID == id {
			return &members[i]
		}
	}
	return nil
}

// Initialize application data
func initData() {
	ensureLogDir()

	// Initialize devices (PCs and Consoles)
	allDevices = []Device{}
	for i := 1; i <= 16; i++ {
		allDevices = append(allDevices, Device{ID: i, Type: "PC", Status: "free", UserID: ""})
	}
	allDevices = append(allDevices, Device{ID: 17, Type: "Console", Status: "free", UserID: ""})
	allDevices = append(allDevices, Device{ID: 18, Type: "Console", Status: "free", UserID: ""})

	// Load active users
	activeUsers = []User{}
	if _, err := os.Stat(userDataFile); !os.IsNotExist(err) {
		f, ferr := os.Open(userDataFile)
		if ferr == nil {
			defer f.Close()
			if json.NewDecoder(f).Decode(&activeUsers) == nil {
				// Update device status for active users
				for _, user := range activeUsers {
					for i := range allDevices {
						if allDevices[i].ID == user.PCID {
							allDevices[i].Status = "occupied"
							allDevices[i].UserID = user.ID
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

// Save user data to file
func saveData() {
	ensureLogDir()
	f, err := os.Create(userDataFile)
	if err != nil {
		fmt.Println("Error creating user data file for save:", err)
		return
	}
	defer f.Close()

	if err := json.NewEncoder(f).Encode(activeUsers); err != nil {
		fmt.Println("Error encoding user data to file:", err)
	}
}

// Helper functions to find users and devices
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

func getDeviceByUserID(uid string) *Device {
	for i := range allDevices {
		if allDevices[i].UserID == uid {
			return &allDevices[i]
		}
	}
	return nil
}

// Register a new user (check-in)
func registerUser(name, id string, deviceID int) error {
	// Validate input
	if _, err := strconv.Atoi(id); err != nil {
		return fmt.Errorf("user ID must be numeric")
	}

	if getUserByID(id) != nil {
		return fmt.Errorf("user ID %s already checked in", id)
	}

	device := getDeviceByID(deviceID)
	if device == nil {
		return fmt.Errorf("device ID %d does not exist", deviceID)
	}

	if device.Status != "free" {
		return fmt.Errorf("device %d is busy", deviceID)
	}

	// Update device and user data
	device.Status = "occupied"
	device.UserID = id
	newUser := User{ID: id, Name: name, CheckInTime: time.Now(), PCID: deviceID}
	activeUsers = append(activeUsers, newUser)

	// Add to member list if new
	if memberByID(id) == nil {
		appendMember(Member{Name: name, ID: id})
	}

	saveData()
	go recordLogEvent(true, newUser, deviceID, nil)
	refreshTrigger <- true
	return nil
}

// Check out a user
func checkoutUser(uid string) error {
	// Validate input
	if _, err := strconv.Atoi(uid); err != nil {
		return fmt.Errorf("user ID must be numeric")
	}

	userToCheckout := getUserByID(uid)
	if userToCheckout == nil {
		return fmt.Errorf("user ID %s not found", uid)
	}

	// Find user index
	idx := -1
	for i, u := range activeUsers {
		if u.ID == uid {
			idx = i
			break
		}
	}

	if idx == -1 {
		return fmt.Errorf("user %s consistency error", uid)
	}

	// Remember check-in time for logging
	originalCheckInTime := userToCheckout.CheckInTime

	// Update device status
	device := getDeviceByUserID(uid)
	loggedDeviceID := userToCheckout.PCID
	if device != nil {
		loggedDeviceID = device.ID
		device.Status = "free"
		device.UserID = ""
	}

	// Remove user from active users
	activeUsers = append(activeUsers[:idx], activeUsers[idx+1:]...)
	saveData()

	go recordLogEvent(false, *userToCheckout, loggedDeviceID, &originalCheckInTime)
	refreshTrigger <- true
	return nil
}

// Format duration
func dur(t time.Time) string {
	return formatDuration(time.Since(t))
}

// DeviceButton represents an interactive device button in the UI
type DeviceButton struct {
	widget.BaseWidget
	device *Device
}

// Create a new device button
func NewDeviceButton(d *Device) *DeviceButton {
	button := &DeviceButton{device: d}
	button.ExtendBaseWidget(button)
	return button
}

// Handle tap/click event
func (button *DeviceButton) Tapped(event *fyne.PointEvent) {
	if button.device.Status == "occupied" {
		// Show checkout dialog for occupied device
		user := getUserByID(button.device.UserID)
		name := "Unknown"
		if user != nil {
			name = user.Name
		}
		dialog.ShowConfirm("Checkout",
			fmt.Sprintf("Checkout %s from %s %d?", name, button.device.Type, button.device.ID),
			func(ok bool) {
				if ok {
					if err := checkoutUser(button.device.UserID); err != nil {
						dialog.ShowError(err, mainWindow)
					}
				}
			}, mainWindow)
	} else {
		// Show check-in dialog for free device
		showCheckInDialogShared(button.device.ID, true)
	}
}

// Implement required interface methods with meaningful names
func (button *DeviceButton) Dragged(event *fyne.DragEvent) {
}

func (button *DeviceButton) DragEnd() {
}

func (button *DeviceButton) MouseIn(event *desktop.MouseEvent) {
}

func (button *DeviceButton) MouseMoved(event *desktop.MouseEvent) {
}

func (button *DeviceButton) MouseOut() {
}

// Create renderer for device button
func (button *DeviceButton) CreateRenderer() fyne.WidgetRenderer {
	// Load appropriate image based on device type
	var imgPath string
	if button.device.Type == "PC" {
		imgPath = filepath.Join(imgBaseDir, "free.png")
	} else {
		imgPath = filepath.Join(imgBaseDir, "console.png")
	}

	if _, err := os.Stat(imgPath); os.IsNotExist(err) {
		fmt.Printf("Warning: %s not found\n", imgPath)
	}

	// Create image and label
	img := canvas.NewImageFromFile(imgPath)
	img.FillMode = canvas.ImageFillContain
	img.SetMinSize(fyne.NewSize(48, 48))

	label := widget.NewLabel(strconv.Itoa(button.device.ID))
	label.TextStyle = fyne.TextStyle{Bold: true}

	// Create container with image and label
	content := container.NewHBox(
		img,
		container.NewPadded(label),
	)

	// Create and initialize renderer
	renderer := &deviceRenderer{
		button:  button,
		img:     img,
		label:   label,
		objects: []fyne.CanvasObject{content},
	}

	renderer.Refresh()
	return renderer
}

func (button *DeviceButton) Refresh() {
	button.BaseWidget.Refresh()
}

// DeviceRenderer handles rendering of device buttons
type deviceRenderer struct {
	button  *DeviceButton
	img     *canvas.Image
	label   *widget.Label
	objects []fyne.CanvasObject
}

func (renderer *deviceRenderer) Layout(size fyne.Size) {
	renderer.objects[0].Resize(size)
}

func (renderer *deviceRenderer) MinSize() fyne.Size {
	imgMin := renderer.img.MinSize()
	labelMin := renderer.label.MinSize()
	return fyne.NewSize(
		imgMin.Width+theme.Padding()+labelMin.Width,
		fyne.Max(imgMin.Height, labelMin.Height),
	)
}

func (renderer *deviceRenderer) Refresh() {
	// Update image based on device status
	var imgPath string
	if renderer.button.device.Status == "free" {
		if renderer.button.device.Type == "PC" {
			imgPath = filepath.Join(imgBaseDir, "free.png")
		} else {
			imgPath = filepath.Join(imgBaseDir, "console.png")
		}
	} else {
		if renderer.button.device.Type == "PC" {
			imgPath = filepath.Join(imgBaseDir, "busy.png")
		} else {
			imgPath = filepath.Join(imgBaseDir, "console_busy.png")
		}
	}

	if _, err := os.Stat(imgPath); os.IsNotExist(err) {
		fmt.Printf("Warning: %s not found\n", imgPath)
	}

	renderer.img.File = imgPath
	renderer.img.Refresh()
	renderer.label.SetText(strconv.Itoa(renderer.button.device.ID))
	renderer.label.Refresh()
	canvas.Refresh(renderer.button)
}

func (renderer *deviceRenderer) Objects() []fyne.CanvasObject {
	return renderer.objects
}

func (renderer *deviceRenderer) Destroy() {
	// Nothing to clean up
}

func (renderer *deviceRenderer) BackgroundColor() color.Color {
	return color.Transparent
}

// Build device room layout
func buildDeviceRoom() *fyne.Container {
	deviceButtons := make(map[int]*DeviceButton)

	// Create button for each device
	for i := range allDevices {
		deviceButtons[allDevices[i].ID] = NewDeviceButton(&allDevices[i])
	}

	// Create layout with rows
	mainContainer := container.NewVBox()

	// Add device button rows with proper spacing
	row1 := container.NewHBox(
		container.NewPadded(deviceButtons[1]),
		container.NewPadded(deviceButtons[2]),
		container.NewPadded(deviceButtons[3]),
	)
	mainContainer.Add(row1)

	row2 := container.NewHBox(
		container.NewPadded(deviceButtons[4]),
		container.NewPadded(deviceButtons[5]),
		container.NewPadded(deviceButtons[6]),
		container.NewPadded(deviceButtons[17]),
	)
	mainContainer.Add(row2)

	row3 := container.NewHBox(
		container.NewPadded(deviceButtons[7]),
		container.NewPadded(deviceButtons[8]),
		container.NewPadded(deviceButtons[9]),
		container.NewPadded(deviceButtons[18]),
	)
	mainContainer.Add(row3)

	row4 := container.NewHBox(
		container.NewPadded(deviceButtons[10]),
		container.NewPadded(deviceButtons[11]),
		container.NewPadded(deviceButtons[12]),
	)
	mainContainer.Add(row4)

	row5 := container.NewHBox(
		container.NewPadded(deviceButtons[13]),
		container.NewPadded(deviceButtons[14]),
		container.NewPadded(deviceButtons[15]),
		container.NewPadded(deviceButtons[16]),
	)
	mainContainer.Add(row5)

	return mainContainer
}

// Build active users view with improved styling
func buildActiveUsersView() fyne.CanvasObject {
	if len(activeUsers) == 0 {
		activeUserList = nil
		return container.NewCenter(
			container.NewPadded(
				widget.NewLabel("No active users."),
			),
		)
	}

	// Create list of active users with better styling
	activeUserList = widget.NewList(
		func() int {
			return len(activeUsers)
		},
		func() fyne.CanvasObject {
			// Create a simple container with labels
			return container.NewVBox(
				widget.NewLabelWithStyle("User Information", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				container.NewHBox(widget.NewLabel("Name:"), widget.NewLabel("")),
				container.NewHBox(widget.NewLabel("ID:"), widget.NewLabel("")),
				container.NewHBox(widget.NewLabel("Device:"), widget.NewLabel("")),
				container.NewHBox(widget.NewLabel("Checked In:"), widget.NewLabel("")),
				container.NewHBox(widget.NewLabel("Usage:"), widget.NewLabel("")),
			)
		},
		func(id widget.ListItemID, item fyne.CanvasObject) {
			if id < len(activeUsers) {
				user := activeUsers[id]
				container := item.(*fyne.Container)

				// Get the labels safely
				nameValueLabel := container.Objects[1].(*fyne.Container).Objects[1].(*widget.Label)
				idValueLabel := container.Objects[2].(*fyne.Container).Objects[1].(*widget.Label)
				deviceValueLabel := container.Objects[3].(*fyne.Container).Objects[1].(*widget.Label)
				checkInValueLabel := container.Objects[4].(*fyne.Container).Objects[1].(*widget.Label)
				usageValueLabel := container.Objects[5].(*fyne.Container).Objects[1].(*widget.Label)

				// Update label values
				nameValueLabel.SetText(user.Name)
				idValueLabel.SetText(user.ID)

				device := getDeviceByID(user.PCID)
				deviceType := "Device"
				if device != nil {
					deviceType = device.Type
				}

				deviceValueLabel.SetText(fmt.Sprintf("%s %d", deviceType, user.PCID))
				checkInValueLabel.SetText(user.CheckInTime.Format("15:04:05"))
				usageValueLabel.SetText(dur(user.CheckInTime))
			}
		},
	)

	return container.NewScroll(activeUserList)
}

// Filter members based on search text
func filterMembers(searchText string) {
	searchText = strings.ToLower(strings.TrimSpace(searchText))
	newDisplayMembers := []Member{}

	if searchText == "" {
		// Show all members when search is empty
		newDisplayMembers = make([]Member, len(members))
		copy(newDisplayMembers, members)
	} else {
		// Filter members by name or ID
		for _, m := range members {
			if strings.Contains(strings.ToLower(m.Name), searchText) ||
				strings.Contains(strings.ToLower(m.ID), searchText) {
				newDisplayMembers = append(newDisplayMembers, m)
			}
		}
	}

	displayMembers = newDisplayMembers
	if memberTable != nil {
		memberTable.Refresh()
	}
}

// Build membership view with search and table
func buildMembershipView() fyne.CanvasObject {
	// Create search entry
	memberSearchEntry = widget.NewEntry()
	memberSearchEntry.SetPlaceHolder("Search Name/ID...")
	memberSearchEntry.OnChanged = filterMembers

	// Create member table
	memberTable = widget.NewTable(
		func() (int, int) {
			return len(displayMembers) + 1, 2
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("")
		},
		func(id widget.TableCellID, o fyne.CanvasObject) {
			label := o.(*widget.Label)

			// Header row styling
			if id.Row == 0 {
				label.TextStyle.Bold = true
				if id.Col == 0 {
					label.SetText("Name")
				} else {
					label.SetText("ID")
				}
				return
			}

			// Data rows
			label.TextStyle.Bold = false
			if id.Row-1 < len(displayMembers) {
				member := displayMembers[id.Row-1]
				if id.Col == 0 {
					label.SetText(member.Name)
				} else {
					label.SetText(member.ID)
				}
			} else {
				label.SetText("")
			}
		},
	)

	// Set column widths
	memberTable.SetColumnWidth(0, 240)
	memberTable.SetColumnWidth(1, 160)

	if displayMembers == nil {
		filterMembers("")
	}

	// Create bordered container for table
	tableContainer := container.NewBorder(
		nil, nil, nil, nil,
		container.NewScroll(memberTable),
	)

	// Add search entry at top
	return container.NewBorder(
		container.NewPadded(memberSearchEntry),
		nil, nil, nil,
		tableContainer,
	)
}

// Show check-in dialog
func showCheckInDialogShared(deviceID int, deviceFixed bool) {
	// Create search entry for finding members
	searchEntry := widget.NewEntry()
	searchEntry.SetPlaceHolder("Search Member...")

	// Create form fields
	nameEntry := widget.NewEntry()
	idEntry := widget.NewEntry()
	deviceEntry := widget.NewEntry()

	// Set device ID if fixed
	if deviceFixed {
		deviceEntry.SetText(strconv.Itoa(deviceID))
		deviceEntry.Disable()
	} else {
		deviceEntry.SetPlaceHolder("Enter Device ID")
	}

	// Setup filtered members list
	var filteredMembers []Member
	var resultsList *widget.List

	resultsList = widget.NewList(
		func() int {
			return len(filteredMembers)
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("")
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i >= 0 && i < len(filteredMembers) {
				o.(*widget.Label).SetText(
					fmt.Sprintf("%s (%s)", filteredMembers[i].Name, filteredMembers[i].ID),
				)
			}
		},
	)

	// Make results scrollable
	scrollableResults := container.NewScroll(resultsList)
	scrollableResults.SetMinSize(fyne.NewSize(380, 80))
	scrollableResults.Hide()

	// Handle selection from results list
	resultsList.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(filteredMembers) {
			selected := filteredMembers[id]
			nameEntry.SetText(selected.Name)
			idEntry.SetText(selected.ID)
			searchEntry.SetText("")
			scrollableResults.Hide()
			resultsList.UnselectAll()
			filteredMembers = []Member{}
			resultsList.Refresh()
		}
	}

	// Handle search entry changes
	searchEntry.OnChanged = func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		nameEntry.Enable()
		idEntry.Enable()

		if s == "" {
			filteredMembers = []Member{}
		} else {
			newFiltered := []Member{}
			for _, m := range members {
				if strings.Contains(strings.ToLower(m.Name), s) ||
					strings.Contains(strings.ToLower(m.ID), s) {
					newFiltered = append(newFiltered, m)
				}
			}
			filteredMembers = newFiltered
		}

		resultsList.Refresh()
		if len(filteredMembers) > 0 {
			scrollableResults.Show()
		} else {
			scrollableResults.Hide()
		}
	}

	// Create dialog reference
	var dialogRef dialog.Dialog

	// Create form for check-in
	formItems := []*widget.FormItem{
		{Text: "Name", Widget: nameEntry},
		{Text: "User ID", Widget: idEntry},
		{Text: "Device ID", Widget: deviceEntry},
	}

	form := &widget.Form{
		Items:      formItems,
		SubmitText: "Check In",
		OnSubmit: func() {
			// Get form values
			userIDStr := strings.TrimSpace(idEntry.Text)
			userName := strings.TrimSpace(nameEntry.Text)

			// Validate inputs
			if userName == "" || userIDStr == "" {
				dialog.ShowError(fmt.Errorf("Name and ID are required"), mainWindow)
				return
			}

			if _, err := strconv.Atoi(userIDStr); err != nil {
				dialog.ShowError(fmt.Errorf("ID must be numeric"), mainWindow)
				return
			}

			// Get device ID
			targetDeviceID := 0
			var err error

			if deviceFixed {
				targetDeviceID = deviceID
			} else {
				pcText := strings.TrimSpace(deviceEntry.Text)
				if pcText == "" {
					dialog.ShowError(fmt.Errorf("Device ID is required"), mainWindow)
					return
				}

				targetDeviceID, err = strconv.Atoi(pcText)
				if err != nil {
					dialog.ShowError(fmt.Errorf("Invalid Device ID"), mainWindow)
					return
				}
			}

			// Register user
			if err = registerUser(userName, userIDStr, targetDeviceID); err != nil {
				dialog.ShowError(err, mainWindow)
				return
			}

			// Close dialog on success
			if dialogRef != nil {
				dialogRef.Hide()
			}
		},
	}

	// Build dialog content
	dialogContent := container.NewVBox(
		widget.NewLabel("Search/Enter Details:"),
		searchEntry,
		scrollableResults,
		form,
	)

	// Create and show dialog
	dialogRef = dialog.NewCustom("Check In", "Cancel", dialogContent, mainWindow)
	dialogRef.Resize(fyne.NewSize(460, 390))
	dialogRef.Show()
}

// Show check-in dialog for any device
func showCheckInDialog() {
	showCheckInDialogShared(0, false)
}

// Show check-out dialog
func showCheckOutDialog() {
	if len(activeUsers) == 0 {
		dialog.ShowInformation("Check Out", "No active users.", mainWindow)
		return
	}

	// Create lists of active users for dropdown
	activeUserDisp := make([]string, len(activeUsers))
	activeUserIDs := make([]string, len(activeUsers))

	for i, u := range activeUsers {
		activeUserDisp[i] = fmt.Sprintf("%s (ID: %s, Device: %d)",
			u.Name, u.ID, u.PCID)
		activeUserIDs[i] = u.ID
	}

	// Create selection dropdown
	idSelector := widget.NewSelectEntry(activeUserDisp)
	idSelector.SetPlaceHolder("Select User")

	// Create dialog reference
	var dialogRef dialog.Dialog

	// Create form for checkout
	form := &widget.Form{
		Items: []*widget.FormItem{
			{Text: "User", Widget: idSelector},
		},
		SubmitText: "Check Out",
		OnSubmit: func() {
			selText := strings.TrimSpace(idSelector.Text)

			if selText == "" {
				dialog.ShowError(fmt.Errorf("No user selected"), mainWindow)
				return
			}

			// Find selected user ID
			var uidToCheckout string
			found := false

			for i, dispStr := range activeUserDisp {
				if dispStr == selText {
					uidToCheckout = activeUserIDs[i]
					found = true
					break
				}
			}

			if !found {
				dialog.ShowError(fmt.Errorf("Invalid selection"), mainWindow)
				return
			}

			// Check out the user
			if err := checkoutUser(uidToCheckout); err != nil {
				dialog.ShowError(err, mainWindow)
				return
			}

			// Close dialog on success
			if dialogRef != nil {
				dialogRef.Hide()
			}
		},
	}

	// Create and show dialog
	dialogRef = dialog.NewCustom("Check Out", "Cancel", form, mainWindow)
	dialogRef.Resize(fyne.NewSize(400, 170))
	dialogRef.Show()
}

func main() {
	// Initialize data
	initData()

	// Ensure image directory exists
	if err := os.MkdirAll(imgBaseDir, 0755); err != nil {
		fmt.Printf("Warning: Unable to create image directory '%s': %v\n", imgBaseDir, err)
	}

	// Create app with classic theme
	myApp := app.New()
	myApp.Settings().SetTheme(newClassicTheme())

	// Create main window
	mainWindow = myApp.NewWindow("Lounge Management System")
	mainWindow.Resize(fyne.NewSize(1080, 720))

	// Build UI components
	deviceRoom = buildDeviceRoom()
	initialActiveUsersView := buildActiveUsersView()
	membershipView := buildMembershipView()
	logView := buildLogView()

	// Create toolbar buttons
	checkInBtn := widget.NewButtonWithIcon("Check In", theme.ContentAddIcon(), showCheckInDialog)
	checkOutBtn := widget.NewButtonWithIcon("Check Out", theme.ContentRemoveIcon(), showCheckOutDialog)
	refreshBtn := widget.NewButtonWithIcon("Refresh Data", theme.ViewRefreshIcon(), func() {
		initData()
		refreshTrigger <- true
	})

	// Create toolbar
	toolbar := container.NewHBox(
		checkInBtn,
		checkOutBtn,
		layout.NewSpacer(),
		refreshBtn,
	)

	// Create status bar labels
	totalLbl := widget.NewLabel("")
	occLbl := widget.NewLabel("")
	userLbl := widget.NewLabel("")

	// Function to update status bar
	updateStatus := func() {
		occupiedCount := 0
		for _, d := range allDevices {
			if d.Status == "occupied" {
				occupiedCount++
			}
		}
		totalLbl.SetText(fmt.Sprintf("Total Devices: %d", len(allDevices)))
		occLbl.SetText(fmt.Sprintf("Occupied: %d", occupiedCount))
		userLbl.SetText(fmt.Sprintf("Active Users: %d", len(activeUsers)))
	}

	updateStatus()

	// Create status bar
	statusBar := container.NewHBox(
		totalLbl,
		widget.NewLabel(" | "),
		occLbl,
		widget.NewLabel(" | "),
		userLbl,
	)

	// Create tabs
	tabs = container.NewAppTabs(
		container.NewTabItem("Device Status", container.NewScroll(deviceRoom)),
		container.NewTabItem("Active Users", initialActiveUsersView),
		container.NewTabItem("Membership", membershipView),
		container.NewTabItem("Log", logView),
	)

	// Store tab indices for reference
	for i, item := range tabs.Items {
		switch item.Text {
		case "Device Status":
			deviceStatusTabIndex = i
		case "Active Users":
			activeUsersTabIndex = i
		case "Membership":
			membershipTabIndex = i
		case "Log":
			logTabIndex = i
		}
	}

	// Set tab location to top
	tabs.SetTabLocation(container.TabLocationTop)

	// Handle tab selection
	tabs.OnSelected = func(ti *container.TabItem) {
		if ti.Text == "Log" && logTabIndex != -1 {
			updateCurrentLogEntriesCache()
			if logTable != nil {
				logTable.Refresh()
			}
			logRefreshPending = false
		} else if ti.Text == "Active Users" && activeUsersTabIndex != -1 {
			tabs.Items[activeUsersTabIndex].Content = buildActiveUsersView()
			tabs.Refresh()
		}
	}

	// Create layout
	topBar := container.NewVBox(toolbar, widget.NewSeparator())
	bottomBar := container.NewVBox(widget.NewSeparator(), statusBar)
	content := container.NewBorder(topBar, bottomBar, nil, nil, tabs)

	// Set content and show window
	mainWindow.SetContent(content)

	// Start background update routine
	go func() {
		// Create tickers for updates
		uiUpdateTicker := time.NewTicker(time.Second)
		dailyLogCheckTicker := time.NewTicker(time.Minute * 5)
		lastLogFileDate := time.Now().Format("2006-01-02")

		defer uiUpdateTicker.Stop()
		defer dailyLogCheckTicker.Stop()

		for {
			select {
			case <-uiUpdateTicker.C:
				// Update active users list every second
				fyne.Do(func() {
					if tabs.Selected() != nil && tabs.Selected().Text == "Active Users" {
						if activeUserList != nil {
							activeUserList.Refresh()
						}
					}
				})

			case <-dailyLogCheckTicker.C:
				// Check for new log file every 5 minutes
				fyne.Do(func() {
					currentDate := time.Now().Format("2006-01-02")
					if currentDate != lastLogFileDate {
						lastLogFileDate = currentDate
						if tabs.Selected() != nil && tabs.Selected().Text == "Log" && logTable != nil {
							updateCurrentLogEntriesCache()
							logTable.Refresh()
						} else {
							logRefreshPending = true
						}
					}
				})

			case <-refreshTrigger:
				// Update UI when triggered
				fyne.Do(func() {
					// Update device status view
					if deviceStatusTabIndex != -1 {
						newDeviceRoomContent := buildDeviceRoom()
						if scroll, ok := tabs.Items[deviceStatusTabIndex].Content.(*container.Scroll); ok {
							scroll.Content = newDeviceRoomContent
							scroll.Refresh()
						} else {
							tabs.Items[deviceStatusTabIndex].Content = container.NewScroll(newDeviceRoomContent)
						}
						deviceRoom = newDeviceRoomContent
					}

					// Update active users view
					if activeUsersTabIndex != -1 {
						tabs.Items[activeUsersTabIndex].Content = buildActiveUsersView()
					}

					// Update status bar
					updateStatus()

					// Update log if pending
					if logRefreshPending {
						updateCurrentLogEntriesCache()
						if tabs.Selected() != nil && tabs.Selected().Text == "Log" && logTable != nil {
							logTable.Refresh()
						}
						logRefreshPending = false
					}

					// Refresh member filter
					filterMembers(memberSearchEntry.Text)

					// Refresh tabs
					tabs.Refresh()
				})
			}
		}
	}()

	mainWindow.ShowAndRun()
}

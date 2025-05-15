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

const (
	userDataFile = "log/active_users.json"
	memberFile   = "membership.csv"
	logDir       = "log"
	imgBaseDir   = "src"
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

type myTheme struct{ fyne.Theme }

func newMyTheme() fyne.Theme { return &myTheme{Theme: theme.LightTheme()} }
func (t *myTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if name == theme.ColorNameSelection {
		return color.NRGBA{R: 0x90, G: 0xCA, B: 0xF9, A: 0x99}
	}
	if name == theme.ColorNameHover {
		return color.NRGBA{R: 0xD0, G: 0xE0, B: 0xF0, A: 0x80}
	}
	return t.Theme.Color(name, variant)
}
func (t *myTheme) Icon(name fyne.ThemeIconName) fyne.Resource { return t.Theme.Icon(name) }
func (t *myTheme) Font(style fyne.TextStyle) fyne.Resource    { return t.Theme.Font(style) }
func (t *myTheme) Size(name fyne.ThemeSizeName) float32       { return t.Theme.Size(name) }

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
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("err open log: %s: %w", filePath, err)
	}
	defer file.Close()
	byteValue, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("err read log: %s: %w", filePath, err)
	}
	var entries []LogEntry
	if len(byteValue) > 0 {
		if err := json.Unmarshal(byteValue, &entries); err != nil {
			return nil, fmt.Errorf("err unmarshal log: %s: %w", filePath, err)
		}
	}
	return entries, nil
}

func writeDailyLogEntries(entries []LogEntry) error {
	filePath := getLogFilePath()
	fileData, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("err marshal log: %w", err)
	}
	return os.WriteFile(filePath, fileData, 0644)
}

func recordLogEvent(isCheckIn bool, user User, deviceID int, originalCheckInTimeForUpdate *time.Time) {
	logFileMutex.Lock()
	defer logFileMutex.Unlock()
	if err := ensureLogDir(); err != nil {
		fmt.Println("Err create log dir:", err)
		return
	}
	entries, err := readDailyLogEntries()
	if err != nil {
		fmt.Println("Err read daily log for update:", err)
		return
	}
	if isCheckIn {
		entries = append(entries, LogEntry{UserName: user.Name, UserID: user.ID, PCID: deviceID, CheckInTime: user.CheckInTime})
	} else {
		found := false
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].UserID == user.ID && entries[i].PCID == deviceID && entries[i].CheckOutTime.IsZero() {
				matchTime := originalCheckInTimeForUpdate == nil || (originalCheckInTimeForUpdate != nil && entries[i].CheckInTime.Equal(*originalCheckInTimeForUpdate))
				if matchTime {
					entries[i].CheckOutTime = time.Now()
					entries[i].UsageTime = formatDuration(entries[i].CheckOutTime.Sub(entries[i].CheckInTime))
					found = true
					break
				}
			}
		}
		if !found {
			fmt.Printf("Err: No matching check-in for user %s (ID: %s) Device %d.\n", user.Name, user.ID, deviceID)
		}
	}
	if err := writeDailyLogEntries(entries); err != nil {
		fmt.Println("Err write daily log:", err)
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
		fmt.Println("Err update log cache:", err)
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
		func(id widget.TableCellID, cell fyne.CanvasObject) {
			label := cell.(*widget.Label)
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
	logTable.SetColumnWidth(0, 180)
	logTable.SetColumnWidth(1, 100)
	logTable.SetColumnWidth(2, 70)
	logTable.SetColumnWidth(3, 150)
	logTable.SetColumnWidth(4, 150)
	logTable.SetColumnWidth(5, 120)
	return container.NewScroll(logTable)
}

func loadMembers() {
	f, err := os.Open(memberFile)
	if err != nil {
		if file, createErr := os.Create(memberFile); createErr == nil {
			file.Close()
		}
		members = []Member{}
		displayMembers = []Member{}
		return
	}
	defer f.Close()
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
		f, ferr := os.Open(userDataFile)
		if ferr == nil {
			defer f.Close()
			if json.NewDecoder(f).Decode(&activeUsers) == nil {
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

func registerUser(name, id string, deviceID int) error {
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

func checkoutUser(uid string) error {
	if _, err := strconv.Atoi(uid); err != nil {
		return fmt.Errorf("user ID must be numeric")
	}
	userToCheckout := getUserByID(uid)
	if userToCheckout == nil {
		return fmt.Errorf("user ID %s not found", uid)
	}
	idx := -1
	for i, u := range activeUsers {
		if u.ID == uid {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("user %s consistency err", uid)
	}
	originalCheckInTime := userToCheckout.CheckInTime
	device := getDeviceByUserID(uid)
	loggedDeviceID := userToCheckout.PCID
	if device != nil {
		loggedDeviceID = device.ID
		device.Status = "free"
		device.UserID = ""
	}
	activeUsers = append(activeUsers[:idx], activeUsers[idx+1:]...)
	saveData()
	go recordLogEvent(false, *userToCheckout, loggedDeviceID, &originalCheckInTime)
	refreshTrigger <- true
	return nil
}

func dur(t time.Time) string { return formatDuration(time.Since(t)) }

type DeviceButton struct {
	widget.BaseWidget
	device *Device
}

func NewDeviceButton(d *Device) *DeviceButton {
	b := &DeviceButton{device: d}
	b.ExtendBaseWidget(b)
	return b
}

func (b *DeviceButton) Tapped(*fyne.PointEvent) {
	if b.device.Status == "occupied" {
		u := getUserByID(b.device.UserID)
		name := "Unknown"
		if u != nil {
			name = u.Name
		}
		dialog.ShowConfirm("Checkout", fmt.Sprintf("Checkout %s from %s %d?", name, b.device.Type, b.device.ID), func(ok bool) {
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
func (b *DeviceButton) Dragged(*fyne.DragEvent)        {}
func (b *DeviceButton) DragEnd()                       {}
func (b *DeviceButton) MouseIn(*desktop.MouseEvent)    {}
func (b *DeviceButton) MouseMoved(*desktop.MouseEvent) {}
func (b *DeviceButton) MouseOut()                      {}
func (b *DeviceButton) CreateRenderer() fyne.WidgetRenderer {
	var imgPath string
	if b.device.Type == "PC" {
		imgPath = filepath.Join(imgBaseDir, "free.png")
	} else {
		imgPath = filepath.Join(imgBaseDir, "console.png")
	}
	if _, err := os.Stat(imgPath); os.IsNotExist(err) {
		fmt.Printf("Warning: %s not found\n", imgPath)
	}
	img := canvas.NewImageFromFile(imgPath)
	img.FillMode = canvas.ImageFillContain
	img.SetMinSize(fyne.NewSize(48, 48))
	label := widget.NewLabel(strconv.Itoa(b.device.ID))
	label.TextStyle = fyne.TextStyle{Bold: true}
	content := container.NewHBox(img, label)
	r := &deviceRenderer{btn: b, img: img, label: label, objs: []fyne.CanvasObject{content}}
	r.Refresh()
	return r
}
func (b *DeviceButton) Refresh() { b.BaseWidget.Refresh() }

type deviceRenderer struct {
	btn   *DeviceButton
	img   *canvas.Image
	label *widget.Label
	objs  []fyne.CanvasObject
}

func (r *deviceRenderer) Layout(s fyne.Size) { r.objs[0].Resize(s) }
func (r *deviceRenderer) MinSize() fyne.Size {
	imgMin := r.img.MinSize()
	labelMin := r.label.MinSize()
	return fyne.NewSize(imgMin.Width+theme.Padding()+labelMin.Width, fyne.Max(imgMin.Height, labelMin.Height))
}

func (r *deviceRenderer) Refresh() {
	var imgPath string
	if r.btn.device.Status == "free" {
		if r.btn.device.Type == "PC" {
			imgPath = filepath.Join(imgBaseDir, "free.png")
		} else {
			imgPath = filepath.Join(imgBaseDir, "console.png")
		}
	} else {
		if r.btn.device.Type == "PC" {
			imgPath = filepath.Join(imgBaseDir, "busy.png")
		} else {
			imgPath = filepath.Join(imgBaseDir, "console_busy.png")
		}
	}
	if _, err := os.Stat(imgPath); os.IsNotExist(err) {
		fmt.Printf("Warning: %s not found\n", imgPath)
	}
	r.img.File = imgPath
	r.img.Refresh()
	r.label.SetText(strconv.Itoa(r.btn.device.ID))
	r.label.Refresh()
	canvas.Refresh(r.btn)
}
func (r *deviceRenderer) Objects() []fyne.CanvasObject { return r.objs }
func (r *deviceRenderer) Destroy()                     {}
func (r *deviceRenderer) BackgroundColor() color.Color { return color.Transparent }

func buildDeviceRoom() *fyne.Container {
	deviceButtons := make(map[int]*DeviceButton)

	for i := range allDevices {
		deviceButtons[allDevices[i].ID] = NewDeviceButton(&allDevices[i])
	}

	mainContainer := container.NewVBox()

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

func buildActiveUsersView() fyne.CanvasObject {
	if len(activeUsers) == 0 {
		activeUserList = nil
		return container.NewCenter(widget.NewLabel("No active users."))
	}
	activeUserList = widget.NewList(
		func() int { return len(activeUsers) },
		func() fyne.CanvasObject {
			return container.New(layout.NewPaddedLayout(),
				container.NewVBox(
					widget.NewLabel("Name:"),
					widget.NewLabel("ID:"),
					widget.NewLabel("Device:"),
					widget.NewLabel("Checked In:"),
					widget.NewLabel("Usage:"),
				),
			)
		},
		func(id widget.ListItemID, item fyne.CanvasObject) {
			if id < len(activeUsers) {
				user := activeUsers[id]
				itemContainer := item.(*fyne.Container).Objects[0].(*fyne.Container)

				nameLabel := itemContainer.Objects[0].(*widget.Label)
				idLabel := itemContainer.Objects[1].(*widget.Label)
				deviceLabel := itemContainer.Objects[2].(*widget.Label)
				checkInLabel := itemContainer.Objects[3].(*widget.Label)
				usageLabel := itemContainer.Objects[4].(*widget.Label)

				nameLabel.SetText(fmt.Sprintf("Name: %s", user.Name))
				idLabel.SetText(fmt.Sprintf("ID: %s", user.ID))
				device := getDeviceByID(user.PCID)
				deviceType := "Dev"
				if device != nil {
					deviceType = device.Type
				}
				deviceLabel.SetText(fmt.Sprintf("%s: %d", deviceType, user.PCID))
				checkInLabel.SetText(fmt.Sprintf("Checked In: %s", user.CheckInTime.Format("15:04:05")))
				usageLabel.SetText(fmt.Sprintf("Usage: %s", dur(user.CheckInTime)))
			}
		},
	)
	return container.NewScroll(activeUserList)
}

func filterMembers(searchText string) {
	searchText = strings.ToLower(strings.TrimSpace(searchText))
	newDisplayMembers := []Member{}
	if searchText == "" {
		newDisplayMembers = make([]Member, len(members))
		copy(newDisplayMembers, members)
	} else {
		for _, m := range members {
			if strings.Contains(strings.ToLower(m.Name), searchText) || strings.Contains(strings.ToLower(m.ID), searchText) {
				newDisplayMembers = append(newDisplayMembers, m)
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
		func(id widget.TableCellID, o fyne.CanvasObject) {
			l := o.(*widget.Label)
			if id.Row == 0 {
				l.TextStyle.Bold = true
				if id.Col == 0 {
					l.SetText("Name")
				} else {
					l.SetText("ID")
				}
				return
			}
			l.TextStyle.Bold = false
			if id.Row-1 < len(displayMembers) {
				m := displayMembers[id.Row-1]
				if id.Col == 0 {
					l.SetText(m.Name)
				} else {
					l.SetText(m.ID)
				}
			} else {
				l.SetText("")
			}
		},
	)
	memberTable.SetColumnWidth(0, 240)
	memberTable.SetColumnWidth(1, 160)
	if displayMembers == nil {
		filterMembers("")
	}
	return container.NewBorder(memberSearchEntry, nil, nil, nil, container.NewScroll(memberTable))
}

func showCheckInDialogShared(deviceID int, deviceFixed bool) {
	searchEntry := widget.NewEntry()
	searchEntry.SetPlaceHolder("Search Member...")
	nameEntry := widget.NewEntry()
	idEntry := widget.NewEntry()
	deviceE := widget.NewEntry()
	if deviceFixed {
		deviceE.SetText(strconv.Itoa(deviceID))
		deviceE.Disable()
	} else {
		deviceE.SetPlaceHolder("Enter Device ID")
	}
	var filteredMembers []Member
	var resultsList *widget.List
	resultsList = widget.NewList(
		func() int { return len(filteredMembers) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i >= 0 && i < len(filteredMembers) {
				o.(*widget.Label).SetText(fmt.Sprintf("%s (%s)", filteredMembers[i].Name, filteredMembers[i].ID))
			}
		},
	)
	scrollableResults := container.NewScroll(resultsList)
	scrollableResults.SetMinSize(fyne.NewSize(380, 80))
	scrollableResults.Hide()
	resultsList.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(filteredMembers) {
			sel := filteredMembers[id]
			nameEntry.SetText(sel.Name)
			idEntry.SetText(sel.ID)
			searchEntry.SetText("")
			scrollableResults.Hide()
			resultsList.UnselectAll()
			filteredMembers = []Member{}
			resultsList.Refresh()
		}
	}
	searchEntry.OnChanged = func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		nameEntry.Enable()
		idEntry.Enable()
		if s == "" {
			filteredMembers = []Member{}
		} else {
			newF := []Member{}
			for _, m := range members {
				if strings.Contains(strings.ToLower(m.Name), s) || strings.Contains(strings.ToLower(m.ID), s) {
					newF = append(newF, m)
				}
			}
			filteredMembers = newF
		}
		resultsList.Refresh()
		if len(filteredMembers) > 0 {
			scrollableResults.Show()
		} else {
			scrollableResults.Hide()
		}
	}
	var dRef dialog.Dialog
	formItems := []*widget.FormItem{{Text: "Name", Widget: nameEntry}, {Text: "User ID", Widget: idEntry}, {Text: "Device ID", Widget: deviceE}}
	form := &widget.Form{
		Items: formItems, SubmitText: "Check In", OnSubmit: func() {
			userIDStr, userName := strings.TrimSpace(idEntry.Text), strings.TrimSpace(nameEntry.Text)
			if userName == "" || userIDStr == "" {
				dialog.ShowError(fmt.Errorf("name & ID req"), mainWindow)
				return
			}
			if _, err := strconv.Atoi(userIDStr); err != nil {
				dialog.ShowError(fmt.Errorf("ID numeric"), mainWindow)
				return
			}
			targetDeviceID := 0
			var err error
			if deviceFixed {
				targetDeviceID = deviceID
			} else {
				pcText := strings.TrimSpace(deviceE.Text)
				if pcText == "" {
					dialog.ShowError(fmt.Errorf("Device ID req"), mainWindow)
					return
				}
				targetDeviceID, err = strconv.Atoi(pcText)
				if err != nil {
					dialog.ShowError(fmt.Errorf("invalid Device ID"), mainWindow)
					return
				}
			}
			if err = registerUser(userName, userIDStr, targetDeviceID); err != nil {
				dialog.ShowError(err, mainWindow)
				return
			}
			if dRef != nil {
				dRef.Hide()
			}
		},
	}
	dialogContent := container.NewVBox(widget.NewLabel("Search/Enter Details:"), searchEntry, scrollableResults, form)
	dRef = dialog.NewCustom("Check In", "Cancel", dialogContent, mainWindow)
	dRef.Resize(fyne.NewSize(460, 390))
	dRef.Show()
}
func showCheckInDialog() { showCheckInDialogShared(0, false) }
func showCheckOutDialog() {
	if len(activeUsers) == 0 {
		dialog.ShowInformation("Check Out", "No active users.", mainWindow)
		return
	}
	activeUserDisp := make([]string, len(activeUsers))
	activeUserIDs := make([]string, len(activeUsers))
	for i, u := range activeUsers {
		activeUserDisp[i] = fmt.Sprintf("%s(ID:%s,Dev:%d)", u.Name, u.ID, u.PCID)
		activeUserIDs[i] = u.ID
	}
	idSel := widget.NewSelectEntry(activeUserDisp)
	idSel.SetPlaceHolder("Select User")
	var dRef dialog.Dialog
	form := &widget.Form{
		Items: []*widget.FormItem{{Text: "User", Widget: idSel}}, SubmitText: "Check Out", OnSubmit: func() {
			selText := strings.TrimSpace(idSel.Text)
			if selText == "" {
				dialog.ShowError(fmt.Errorf("no user selected"), mainWindow)
				return
			}
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
				dialog.ShowError(fmt.Errorf("invalid selection"), mainWindow)
				return
			}
			if err := checkoutUser(uidToCheckout); err != nil {
				dialog.ShowError(err, mainWindow)
				return
			}
			if dRef != nil {
				dRef.Hide()
			}
		},
	}
	dRef = dialog.NewCustom("Check Out", "Cancel", form, mainWindow)
	dRef.Resize(fyne.NewSize(400, 170))
	dRef.Show()
}

func main() {
	initData()
	if err := os.MkdirAll(imgBaseDir, 0755); err != nil {
		fmt.Printf("Warn: no img dir '%s':%v\n", imgBaseDir, err)
	}
	myApp := app.New()
	myApp.Settings().SetTheme(newMyTheme())
	mainWindow = myApp.NewWindow("Lounge Management")
	mainWindow.Resize(fyne.NewSize(1080, 720))
	deviceRoom = buildDeviceRoom()
	initialActiveUsersView := buildActiveUsersView()
	membershipView := buildMembershipView()
	logView := buildLogView()
	checkInBtn := widget.NewButtonWithIcon("Check In", theme.ContentAddIcon(), showCheckInDialog)
	checkOutBtn := widget.NewButtonWithIcon("Check Out", theme.ContentRemoveIcon(), showCheckOutDialog)
	refreshBtn := widget.NewButtonWithIcon("Refresh Data", theme.ViewRefreshIcon(), func() {
		initData()
		refreshTrigger <- true
	})
	toolbar := container.NewHBox(checkInBtn, checkOutBtn, layout.NewSpacer(), refreshBtn)
	totalLbl, occLbl, userLbl := widget.NewLabel(""), widget.NewLabel(""), widget.NewLabel("")
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
	statusBar := container.NewHBox(totalLbl, widget.NewLabel(" | "), occLbl, widget.NewLabel(" | "), userLbl)
	tabs = container.NewAppTabs(
		container.NewTabItem("Device Status", container.NewScroll(deviceRoom)),
		container.NewTabItem("Active Users", initialActiveUsersView),
		container.NewTabItem("Membership", membershipView),
		container.NewTabItem("Log", logView),
	)
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
	tabs.SetTabLocation(container.TabLocationTop) // Changed to TabLocationTop
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
	topBar := container.NewVBox(toolbar, widget.NewSeparator())
	bottomBar := container.NewVBox(widget.NewSeparator(), statusBar)
	content := container.NewBorder(topBar, bottomBar, nil, nil, tabs)
	mainWindow.SetContent(content)
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
						if activeUserList != nil {
							activeUserList.Refresh()
						}
					}
				})
			case <-dailyLogCheckTicker.C:
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
				fyne.Do(func() {
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
					if activeUsersTabIndex != -1 {
						tabs.Items[activeUsersTabIndex].Content = buildActiveUsersView()
					}
					updateStatus()
					if logRefreshPending {
						updateCurrentLogEntriesCache()
						if tabs.Selected() != nil && tabs.Selected().Text == "Log" && logTable != nil {
							logTable.Refresh()
						}
						logRefreshPending = false
					}
					filterMembers(memberSearchEntry.Text)
					tabs.Refresh()
				})
			}
		}
	}()
	mainWindow.ShowAndRun()
}

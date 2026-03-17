APP_NAME = Lounge
APP_ID = com.briar.lounge
APP_VERSION = 1.2.0
ICON = icon.png

.PHONY: all win clean

all:
	go build -o lounge .

# Build Windows amd64 exe using fyne-cross
win:
	fyne-cross windows -arch=amd64 \
		--icon $(ICON) \
		--app-id $(APP_ID) \
		--name "$(APP_NAME)" \
		--app-version $(APP_VERSION) \
		-output lounge

# Remove build artifacts
clean:
	rm -rf fyne-cross lounge

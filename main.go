package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/faiface/beep"
	"github.com/faiface/beep/mp3"
	"github.com/faiface/beep/speaker"
)

//go:embed all:sounds
var assets embed.FS

type User struct {
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func loadUsers(path string) ([]User, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var users []User
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, err
	}
	base := filepath.Dir(path)
	for i := range users {
		if !filepath.IsAbs(users[i].Avatar) {
			users[i].Avatar = filepath.Join(base, users[i].Avatar)
		}
	}
	return users, nil
}

// silentStream streams silence indefinitely, keeping the audio device warm.
type silentStream struct{}

func (silentStream) Stream(samples [][2]float64) (int, bool) {
	for i := range samples {
		samples[i] = [2]float64{}
	}
	return len(samples), true
}
func (silentStream) Err() error { return nil }

// soundBank holds pre-decoded audio buffers so playback is instant.
type soundBank struct {
	spin   *beep.Buffer
	reveal *beep.Buffer
	rate   beep.SampleRate
	inited bool
}

func loadSoundBank() *soundBank {
	sb := &soundBank{}

	loadOne := func(path string) *beep.Buffer {
		f, err := assets.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		s, format, err := mp3.Decode(f)
		if err != nil {
			return nil
		}
		defer s.Close()
		if !sb.inited {
			sb.rate = format.SampleRate
			// Small buffer (50 ms) reduces queuing latency; silence keepalive
			// below prevents the device from going idle between sounds.
			speaker.Init(sb.rate, sb.rate.N(time.Second/20))
			speaker.Play(silentStream{})
			sb.inited = true
		}
		var src beep.Streamer = s
		if format.SampleRate != sb.rate {
			src = beep.Resample(4, format.SampleRate, sb.rate, s)
			format.SampleRate = sb.rate
		}
		buf := beep.NewBuffer(format)
		buf.Append(src)
		return buf
	}

	sb.spin = loadOne("sounds/mk64.mp3")
	sb.reveal = loadOne("sounds/reveal.mp3")
	return sb
}

func (sb *soundBank) playSpin() {
	if sb.spin == nil {
		return
	}
	speaker.Play(sb.spin.Streamer(0, sb.spin.Len()))
}

func (sb *soundBank) playReveal() {
	if sb.reveal == nil {
		return
	}
	speaker.Play(sb.reveal.Streamer(0, sb.reveal.Len()))
}

// --- confetti ---

type particle struct {
	x, y   float64
	vx, vy float64
	angle  float64
	spin   float64
	col    color.RGBA
	alive  bool
}

var (
	confMu    sync.Mutex
	confParts []particle
)

var confettiColors = []color.RGBA{
	{R: 220, G: 50, B: 50, A: 255},
	{R: 50, G: 200, B: 80, A: 255},
	{R: 60, G: 120, B: 220, A: 255},
	{R: 230, G: 190, B: 40, A: 255},
	{R: 190, G: 60, B: 220, A: 255},
	{R: 50, G: 210, B: 210, A: 255},
	{R: 230, G: 130, B: 40, A: 255},
	{R: 230, G: 100, B: 170, A: 255},
}

func launchConfetti(raster *canvas.Raster, pixW, pixH float64) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	const count = 140
	parts := make([]particle, count)
	for i := range parts {
		fromRight := i >= count/2
		var x, vx float64
		if fromRight {
			x = pixW
			vx = -(rng.Float64()*12 + 3)
		} else {
			x = 0
			vx = rng.Float64()*12 + 3
		}
		sign := 1.0
		if rng.Intn(2) == 0 {
			sign = -1
		}
		parts[i] = particle{
			x:     x,
			y:     pixH,
			vx:    vx,
			vy:    -(rng.Float64()*12 + 10),
			angle: rng.Float64() * math.Pi * 2,
			spin:  (rng.Float64()*0.18 + 0.04) * sign,
			col:   confettiColors[rng.Intn(len(confettiColors))],
			alive: true,
		}
	}

	confMu.Lock()
	confParts = parts
	confMu.Unlock()

	go func() {
		ticker := time.NewTicker(16 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			confMu.Lock()
			alive := 0
			for i := range confParts {
				if !confParts[i].alive {
					continue
				}
				confParts[i].vy += 0.45
				confParts[i].x += confParts[i].vx
				confParts[i].y += confParts[i].vy
				confParts[i].angle += confParts[i].spin
				if confParts[i].y > pixH+30 {
					confParts[i].alive = false
				} else {
					alive++
				}
			}
			done := alive == 0
			confMu.Unlock()

			fyne.Do(func() { raster.Refresh() })
			if done {
				return
			}
		}
	}()
}

func drawRotatedRect(img *image.RGBA, cx, cy, w, h, angle float64, col color.RGBA) {
	cos := math.Cos(angle)
	sin := math.Sin(angle)
	hw, hh := w/2, h/2
	bounds := img.Bounds()

	absW := math.Abs(hw*cos) + math.Abs(hh*sin)
	absH := math.Abs(hw*sin) + math.Abs(hh*cos)

	x0 := int(cx-absW) - 1
	x1 := int(cx+absW) + 1
	y0 := int(cy-absH) - 1
	y1 := int(cy+absH) + 1

	for py := y0; py <= y1; py++ {
		for px := x0; px <= x1; px++ {
			if px < bounds.Min.X || px >= bounds.Max.X || py < bounds.Min.Y || py >= bounds.Max.Y {
				continue
			}
			dx := float64(px) - cx
			dy := float64(py) - cy
			if math.Abs(dx*cos+dy*sin) <= hw && math.Abs(-dx*sin+dy*cos) <= hh {
				img.SetRGBA(px, py, col)
			}
		}
	}
}

// --- UI ---

func avatarCard(u User, size float32) fyne.CanvasObject {
	img := canvas.NewImageFromFile(u.Avatar)
	img.FillMode = canvas.ImageFillContain
	img.SetMinSize(fyne.NewSize(size, size))
	lbl := widget.NewLabel(u.Name)
	lbl.Alignment = fyne.TextAlignCenter
	lbl.Wrapping = fyne.TextWrapWord
	return container.NewVBox(img, lbl)
}

func buildGrid(users []User) fyne.CanvasObject {
	const cardSize float32 = 80
	shown := users
	extra := 0
	if len(users) > 10 {
		shown, extra = users[:10], len(users)-10
	}
	cards := make([]fyne.CanvasObject, len(shown))
	for i, u := range shown {
		cards[i] = avatarCard(u, cardSize)
	}
	grid := container.New(layout.NewGridLayoutWithColumns(5), cards...)
	if extra > 0 {
		lbl := widget.NewLabel(fmt.Sprintf("+%d more", extra))
		lbl.Alignment = fyne.TextAlignCenter
		return container.NewVBox(grid, lbl)
	}
	return grid
}

func main() {
	a := app.New()
	w := a.NewWindow("Pick The Victim")
	w.Resize(fyne.NewSize(580, 500))

	sounds := loadSoundBank()

	users, usersErr := loadUsers(filepath.Join(exeDir(), "users.json"))

	initRng := rand.New(rand.NewSource(time.Now().UnixNano()))
	initRng.Shuffle(len(users), func(i, j int) { users[i], users[j] = users[j], users[i] })

	var topGrid fyne.CanvasObject
	if len(users) > 0 {
		topGrid = container.NewCenter(buildGrid(users))
	} else {
		msg := "(place users.json next to this binary)"
		if usersErr != nil {
			msg = usersErr.Error()
		}
		lbl := widget.NewLabel(msg)
		lbl.Alignment = fyne.TextAlignCenter
		topGrid = container.NewCenter(lbl)
	}

	spinImg := canvas.NewImageFromFile("")
	spinImg.FillMode = canvas.ImageFillContain
	spinImg.SetMinSize(fyne.NewSize(130, 130))
	spinImg.Hide()

	spinLbl := canvas.NewText("", theme.ForegroundColor())
	spinLbl.TextSize = 40
	spinLbl.Alignment = fyne.TextAlignCenter
	spinLbl.TextStyle = fyne.TextStyle{Bold: true}

	spinArea := container.NewVBox(
		container.NewCenter(spinImg),
		container.NewCenter(spinLbl),
	)

	pickBtn := widget.NewButton("Pick The Victim", nil)
	pickBtn.Importance = widget.HighImportance
	if len(users) == 0 {
		pickBtn.Disable()
	}

	var rasterW, rasterH int
	confRaster := canvas.NewRaster(func(pw, ph int) image.Image {
		rasterW, rasterH = pw, ph
		img := image.NewRGBA(image.Rect(0, 0, pw, ph))
		confMu.Lock()
		defer confMu.Unlock()
		for _, p := range confParts {
			if !p.alive {
				continue
			}
			drawRotatedRect(img, p.x, p.y, 12, 5, p.angle, p.col)
		}
		return img
	})

	var spinning bool

	pickBtn.OnTapped = func() {
		if spinning || len(users) == 0 {
			return
		}
		spinning = true
		pickBtn.Disable()

		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		winner := users[rng.Intn(len(users))]

		go func() {
			const total = 5 * time.Second
			const startMs = 50.0
			const endMs = 500.0

			sounds.playSpin()

			start := time.Now()
			for {
				elapsed := time.Since(start)
				if elapsed >= total {
					break
				}
				t := float64(elapsed) / float64(total)
				interval := time.Duration(startMs*math.Pow(endMs/startMs, t)) * time.Millisecond

				u := users[rng.Intn(len(users))]
				fyne.Do(func() {
					spinLbl.Text = u.Name
					spinLbl.Refresh()
					spinImg.File = u.Avatar
					spinImg.Show()
					spinImg.Refresh()
				})

				time.Sleep(interval)
			}

			fyne.Do(func() {
				spinLbl.Text = winner.Name
				spinLbl.Refresh()
				spinImg.File = winner.Avatar
				spinImg.Show()
				spinImg.Refresh()

				launchConfetti(confRaster, float64(rasterW), float64(rasterH))
			})
			sounds.playReveal()

			fyne.Do(func() {
				spinning = false
				pickBtn.Enable()
			})
		}()
	}

	content := container.NewPadded(container.NewVBox(
		topGrid,
		widget.NewSeparator(),
		spinArea,
		widget.NewSeparator(),
		container.NewCenter(pickBtn),
	))

	w.SetContent(container.NewStack(content, confRaster))
	w.ShowAndRun()
}

package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"strconv"
)

const defaultPins = 300
const defaultMinDistance = 20
const defaultMaxLines = 4000
const defaultLineWeight = 20
const workingSize = 500

// ColorLayer represents one thread color's line sequence
type ColorLayer struct {
	Color        string `json:"color"`
	LineSequence []int  `json:"line_sequence"`
}

type GenerateResponse struct {
	PinCoords    [][2]float64 `json:"pin_coords"`
	LineSequence []int        `json:"line_sequence"`
	ImgSize      int          `json:"img_size"`
	NumPins      int          `json:"num_pins"`
	MaxLines     int          `json:"max_lines"`
	Mode         string       `json:"mode"`
	ColorLayers  []ColorLayer `json:"color_layers,omitempty"`
}

func main() {
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.HandleFunc("/api/generate", handleGenerate)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/index.html")
	})

	fmt.Println("String Art Generator server starting on http://localhost:8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}

func handleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "File too large or invalid form", http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "No image uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()

	pins := parseIntParam(r.FormValue("pins"), defaultPins)
	maxLines := parseIntParam(r.FormValue("maxLines"), defaultMaxLines)
	lineWeight := parseIntParam(r.FormValue("lineWeight"), defaultLineWeight)
	minDistance := parseIntParam(r.FormValue("minDistance"), defaultMinDistance)
	mode := r.FormValue("mode") // "bw" or "color"
	if mode == "" {
		mode = "bw"
	}

	if pins < 50 {
		pins = 50
	} else if pins > 500 {
		pins = 500
	}
	if maxLines < 100 {
		maxLines = 100
	} else if maxLines > 10000 {
		maxLines = 10000
	}
	if lineWeight < 1 {
		lineWeight = 1
	} else if lineWeight > 100 {
		lineWeight = 100
	}

	pinCoords := calculatePinCoords(pins, workingSize)
	lineCacheX, lineCacheY := precalculateLines(pins, minDistance, pinCoords)

	if mode == "color" {
		channels, err := processImageColor(file)
		if err != nil {
			http.Error(w, "Failed to process image: "+err.Error(), http.StatusBadRequest)
			return
		}

		// CMYK channel names and CSS colors
		// We invert: thread color is the ink color
		// C channel = areas needing cyan thread, M = magenta, Y = yellow, K = black
		colorNames := []string{"cyan", "magenta", "yellow", "black"}
		cssColors := []string{
			"rgba(0,180,220,0.6)",   // cyan
			"rgba(200,0,100,0.6)",   // magenta
			"rgba(200,180,0,0.5)",   // yellow
			"rgba(0,0,0,0.7)",       // black
		}

		// Lines per channel: split maxLines among 4 channels
		// Black gets more since it carries most detail
		linesK := maxLines * 35 / 100
		linesC := maxLines * 22 / 100
		linesM := maxLines * 22 / 100
		linesY := maxLines * 21 / 100
		linesPerChannel := []int{linesC, linesM, linesY, linesK}

		layers := make([]ColorLayer, 4)
		for i := 0; i < 4; i++ {
			seq := calculateLines(pins, linesPerChannel[i], lineWeight, minDistance, channels[i], lineCacheX, lineCacheY)
			layers[i] = ColorLayer{
				Color:        cssColors[i],
				LineSequence: seq,
			}
			_ = colorNames[i]
		}

		resp := GenerateResponse{
			PinCoords:   pinCoords,
			ImgSize:     workingSize,
			NumPins:     pins,
			MaxLines:    maxLines,
			Mode:        "color",
			ColorLayers: layers,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	} else {
		grayscale, err := processImage(file)
		if err != nil {
			http.Error(w, "Failed to process image: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Convert grayscale to error: 255 - brightness = darkness needed
		for i := range grayscale {
			grayscale[i] = 255.0 - grayscale[i]
		}

		lineSequence := calculateLines(pins, maxLines, lineWeight, minDistance, grayscale, lineCacheX, lineCacheY)

		resp := GenerateResponse{
			PinCoords:    pinCoords,
			LineSequence: lineSequence,
			ImgSize:      workingSize,
			NumPins:      pins,
			MaxLines:     maxLines,
			Mode:         "bw",
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func parseIntParam(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}

// decodeAndCrop decodes an image and returns the cropped/resized raw RGBA data
func decodeAndCrop(file io.Reader) (image.Image, int, int, int, int, error) {
	img, _, err := image.Decode(file)
	if err != nil {
		return nil, 0, 0, 0, 0, err
	}

	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	cropSize := w
	if h < w {
		cropSize = h
	}
	xOffset := (w - cropSize) / 2
	yOffset := (h - cropSize) / 2

	return img, xOffset, yOffset, cropSize, cropSize, nil
}

func processImage(file io.Reader) ([]float64, error) {
	img, xOffset, yOffset, cropW, _, err := decodeAndCrop(file)
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()

	pixels := make([]float64, workingSize*workingSize)
	for y := 0; y < workingSize; y++ {
		for x := 0; x < workingSize; x++ {
			srcX := xOffset + x*cropW/workingSize + bounds.Min.X
			srcY := yOffset + y*cropW/workingSize + bounds.Min.Y

			r, g, b, _ := img.At(srcX, srcY).RGBA()
			avg := float64(r+g+b) / 3.0 / 257.0
			pixels[y*workingSize+x] = avg
		}
	}

	applyCircleMask(pixels)
	return pixels, nil
}

// processImageColor returns 4 channels: C, M, Y, K
// Each channel is a "darkness" map (0=no ink needed, 255=full ink)
func processImageColor(file io.Reader) ([4][]float64, error) {
	img, xOffset, yOffset, cropW, _, err := decodeAndCrop(file)
	if err != nil {
		return [4][]float64{}, err
	}
	bounds := img.Bounds()
	size := workingSize * workingSize

	chanC := make([]float64, size)
	chanM := make([]float64, size)
	chanY := make([]float64, size)
	chanK := make([]float64, size)

	for y := 0; y < workingSize; y++ {
		for x := 0; x < workingSize; x++ {
			srcX := xOffset + x*cropW/workingSize + bounds.Min.X
			srcY := yOffset + y*cropW/workingSize + bounds.Min.Y

			ri, gi, bi, _ := img.At(srcX, srcY).RGBA()
			// Scale to 0-1
			rf := float64(ri) / 65535.0
			gf := float64(gi) / 65535.0
			bf := float64(bi) / 65535.0

			// RGB to CMY
			c := 1.0 - rf
			m := 1.0 - gf
			yy := 1.0 - bf

			// Under-color removal: extract K
			k := math.Min(c, math.Min(m, yy))

			// Avoid division by zero for pure black
			if k >= 1.0 {
				c, m, yy = 0, 0, 0
			} else {
				c = (c - k) / (1.0 - k)
				m = (m - k) / (1.0 - k)
				yy = (yy - k) / (1.0 - k)
			}

			idx := y*workingSize + x
			// Store as "darkness" values 0-255 (how much thread is needed)
			chanC[idx] = c * 255.0
			chanM[idx] = m * 255.0
			chanY[idx] = yy * 255.0
			chanK[idx] = k * 255.0
		}
	}

	// Apply circle mask (set to 0 = no ink needed outside circle)
	center := float64(workingSize) / 2.0
	radius := center - 0.5
	for y := 0; y < workingSize; y++ {
		for x := 0; x < workingSize; x++ {
			dx := float64(x) - center
			dy := float64(y) - center
			if dx*dx+dy*dy > radius*radius {
				idx := y*workingSize + x
				chanC[idx] = 0
				chanM[idx] = 0
				chanY[idx] = 0
				chanK[idx] = 0
			}
		}
	}

	return [4][]float64{chanC, chanM, chanY, chanK}, nil
}

func applyCircleMask(pixels []float64) {
	center := float64(workingSize) / 2.0
	radius := center - 0.5
	for y := 0; y < workingSize; y++ {
		for x := 0; x < workingSize; x++ {
			dx := float64(x) - center
			dy := float64(y) - center
			if dx*dx+dy*dy > radius*radius {
				pixels[y*workingSize+x] = 255.0
			}
		}
	}
}

func calculatePinCoords(pins, imgSize int) [][2]float64 {
	coords := make([][2]float64, pins)
	center := float64(imgSize) / 2.0
	radius := float64(imgSize)/2.0 - 0.5

	for i := 0; i < pins; i++ {
		angle := 2.0 * math.Pi * float64(i) / float64(pins)
		coords[i] = [2]float64{
			math.Floor(center + radius*math.Cos(angle)),
			math.Floor(center + radius*math.Sin(angle)),
		}
	}
	return coords
}

func linspace(a, b float64, n int) []float64 {
	if n < 2 {
		if n == 1 {
			return []float64{a}
		}
		return []float64{}
	}
	ret := make([]float64, n)
	nm1 := float64(n - 1)
	for i := n - 1; i >= 0; i-- {
		ret[i] = math.Floor((float64(i)*b + (nm1-float64(i))*a) / nm1)
	}
	return ret
}

func precalculateLines(pins, minDistance int, pinCoords [][2]float64) ([][]float64, [][]float64) {
	size := pins * pins
	lineCacheX := make([][]float64, size)
	lineCacheY := make([][]float64, size)

	for i := 0; i < pins; i++ {
		for j := i + minDistance; j < pins; j++ {
			x0 := pinCoords[i][0]
			y0 := pinCoords[i][1]
			x1 := pinCoords[j][0]
			y1 := pinCoords[j][1]

			d := int(math.Floor(math.Sqrt((x1-x0)*(x1-x0) + (y1-y0)*(y1-y0))))
			xs := linspace(x0, x1, d)
			ys := linspace(y0, y1, d)

			lineCacheY[j*pins+i] = ys
			lineCacheY[i*pins+j] = ys
			lineCacheX[j*pins+i] = xs
			lineCacheX[i*pins+j] = xs
		}
	}
	return lineCacheX, lineCacheY
}

// calculateLines works for both BW and color modes.
// For BW: sourceImage is grayscale (0=black, 255=white), error = 255 - grayscale.
// For color: sourceImage is already the "darkness" channel (0=none, 255=full), used directly as error.
func calculateLines(pins, maxLines, lineWeight, minDistance int, sourceImage []float64, lineCacheX, lineCacheY [][]float64) []int {
	imgSize := workingSize
	imgSizeSq := imgSize * imgSize

	errorArr := make([]float64, imgSizeSq)
	copy(errorArr, sourceImage)

	lineSequence := make([]int, 1, maxLines+1)
	lineSequence[0] = 0
	currentPin := 0
	lastPins := make([]int, 0, 24)

	for l := 0; l < maxLines; l++ {
		bestPin := -1
		maxErr := float64(-1)

		for offset := minDistance; offset < pins-minDistance; offset++ {
			testPin := (currentPin + offset) % pins
			if containsInt(lastPins, testPin) {
				continue
			}

			idx := testPin*pins + currentPin
			xs := lineCacheX[idx]
			ys := lineCacheY[idx]
			if xs == nil || ys == nil {
				continue
			}

			lineErr := getLineErr(errorArr, ys, xs, imgSize)
			if lineErr > maxErr {
				maxErr = lineErr
				bestPin = testPin
			}
		}

		if bestPin == -1 {
			break
		}

		lineSequence = append(lineSequence, bestPin)

		idx := bestPin*pins + currentPin
		xs := lineCacheX[idx]
		ys := lineCacheY[idx]
		for i := range xs {
			py := int(ys[i])
			px := int(xs[i])
			if py >= 0 && py < imgSize && px >= 0 && px < imgSize {
				v := py*imgSize + px
				errorArr[v] -= float64(lineWeight)
				if errorArr[v] < 0 {
					errorArr[v] = 0
				} else if errorArr[v] > 255 {
					errorArr[v] = 255
				}
			}
		}

		lastPins = append(lastPins, bestPin)
		if len(lastPins) > 20 {
			lastPins = lastPins[1:]
		}
		currentPin = bestPin
	}

	return lineSequence
}

func getLineErr(errorArr []float64, coordsY, coordsX []float64, imgSize int) float64 {
	sum := 0.0
	for i := 0; i < len(coordsY); i++ {
		py := int(coordsY[i])
		px := int(coordsX[i])
		if py >= 0 && py < imgSize && px >= 0 && px < imgSize {
			sum += errorArr[py*imgSize+px]
		}
	}
	return sum
}

func containsInt(arr []int, num int) bool {
	for _, v := range arr {
		if v == num {
			return true
		}
	}
	return false
}

func init() {
	image.RegisterFormat("jpeg", "\xff\xd8", jpeg.Decode, jpeg.DecodeConfig)
	image.RegisterFormat("png", "\x89PNG", png.Decode, png.DecodeConfig)
}

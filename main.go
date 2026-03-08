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

type GenerateResponse struct {
	PinCoords    [][2]float64 `json:"pin_coords"`
	LineSequence []int        `json:"line_sequence"`
	ImgSize      int          `json:"img_size"`
	NumPins      int          `json:"num_pins"`
	MaxLines     int          `json:"max_lines"`
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

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10MB max

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

	grayscale, err := processImage(file)
	if err != nil {
		http.Error(w, "Failed to process image: "+err.Error(), http.StatusBadRequest)
		return
	}

	pinCoords := calculatePinCoords(pins, workingSize)
	lineCacheX, lineCacheY := precalculateLines(pins, minDistance, pinCoords)
	lineSequence := calculateLines(pins, maxLines, lineWeight, minDistance, grayscale, lineCacheX, lineCacheY)

	resp := GenerateResponse{
		PinCoords:    pinCoords,
		LineSequence: lineSequence,
		ImgSize:      workingSize,
		NumPins:      pins,
		MaxLines:     maxLines,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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

func processImage(file io.Reader) ([]float64, error) {
	img, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}

	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	// Square crop from center
	cropSize := w
	if h < w {
		cropSize = h
	}
	xOffset := (w - cropSize) / 2
	yOffset := (h - cropSize) / 2

	// Resize to workingSize x workingSize and convert to grayscale
	pixels := make([]float64, workingSize*workingSize)
	for y := 0; y < workingSize; y++ {
		for x := 0; x < workingSize; x++ {
			// Nearest-neighbor sampling from the cropped region
			srcX := xOffset + x*cropSize/workingSize
			srcY := yOffset + y*cropSize/workingSize
			srcX += bounds.Min.X
			srcY += bounds.Min.Y

			r, g, b, _ := img.At(srcX, srcY).RGBA()
			// Average RGB for grayscale, scale from 16-bit to 8-bit
			avg := float64(r+g+b) / 3.0 / 257.0
			pixels[y*workingSize+x] = avg
		}
	}

	// Circle mask: set pixels outside circle to white (255)
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

	return pixels, nil
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

func calculateLines(pins, maxLines, lineWeight, minDistance int, sourceImage []float64, lineCacheX, lineCacheY [][]float64) []int {
	imgSize := workingSize
	imgSizeSq := imgSize * imgSize

	// error = 255 - sourceImage (how much "darkness" is needed)
	errorArr := make([]float64, imgSizeSq)
	for i := 0; i < imgSizeSq; i++ {
		errorArr[i] = 255.0 - sourceImage[i]
	}

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

		// Subtract line weight from error array, clamped to [0, 255]
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

//Wasming
// compile: GOOS=js GOARCH=wasm go build -o main.wasm ./main.go
package main

import (
	"fmt"
	"math"
	"sort"
	"syscall/js"
	"time"

	"go.uber.org/atomic"
)

type matrix []float64

type Point struct {
	Label      string
	LabelAlign string
	X          float64
	Y          float64
	Z          float64
}

type Edge []int
type Surface []int

type Object struct {
	C         string // Colour of the object
	P         []Point
	E         []Edge    // List of points to connect by edges
	S         []Surface // List of points to connect in order, to create a surface
	DrawOrder int       // Draw order for the object
	Name      string
}

type OperationType int

const (
	ROTATE OperationType = iota
	SCALE
	TRANSLATE
)

type Operation struct {
	op OperationType
	t  int32 // Number of milliseconds the operation should take
	f  int32 // Number of display frames the operation should be broken into
	X  float64
	Y  float64
	Z  float64
}

type drawOrder struct {
	order    int // Draw order for an object
	spaceNum int // Index of the object in the worldSpace slice
}

type drawOrderSlice []drawOrder

func (o drawOrder) String() string {
	return fmt.Sprintf("Object: %v, Order: %v", o.spaceNum, o.order)
}

func (o drawOrderSlice) Len() int {
	return len(o)
}

func (o drawOrderSlice) Swap(i, j int) {
	o[i], o[j] = o[j], o[i]
}

func (o drawOrderSlice) Less(i, j int) bool {
	return o[i].order < o[j].order
}

const (
	sourceURL = "https://github.com/justinclift/wasmGraph4"
)

var (
	// The empty world space
	worldSpace []Object

	// The point objects
	axes = Object{
		C:         "grey",
		DrawOrder: 0,
		Name:      "axes",
		P: []Point{
			{X: -0.1, Y: 0.1, Z: 0.0},
			{X: -0.1, Y: 10, Z: 0.0},
			{X: 0.1, Y: 10, Z: 0.0},
			{X: 0.1, Y: 0.1, Z: 0.0},
			{X: 10, Y: 0.1, Z: 0.0},
			{X: 10, Y: -0.1, Z: 0.0},
			{X: 0.1, Y: -0.1, Z: 0.0},
			{X: 0.1, Y: -10, Z: 0.0},
			{X: -0.1, Y: -10, Z: 0.0},
			{X: -0.1, Y: -0.1, Z: 0.0},
			{X: -10, Y: -0.1, Z: 0.0},
			{X: -10, Y: 0.1, Z: 0.0},
			{X: 10, Y: -1.0, Z: 0.0, Label: "X", LabelAlign: "center"},
			{X: -10, Y: -1.0, Z: 0.0, Label: "-X", LabelAlign: "center"},
			{X: 0.0, Y: 10.5, Z: 0.0, Label: "Y", LabelAlign: "center"},
			{X: 0.0, Y: -11, Z: 0.0, Label: "-Y", LabelAlign: "center"},
		},
		E: []Edge{
			{0, 1},
			{1, 2},
			{2, 3},
			{3, 4},
			{4, 5},
			{5, 6},
			{6, 7},
			{7, 8},
			{8, 9},
			{9, 10},
			{10, 11},
			{11, 0},
		},
		S: []Surface{
			{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
		},
	}

	// The 4x4 identity matrix
	identityMatrix = matrix{
		1, 0, 0, 0,
		0, 1, 0, 0,
		0, 0, 1, 0,
		0, 0, 0, 1,
	}

	// Initialise the transform matrix with the identity matrix
	transformMatrix = identityMatrix

	// FIFO queue
	queue        chan Operation
	renderActive *atomic.Bool

	width, height       float64
	graphWidth          float64
	graphHeight         float64
	cCall, kCall, mCall js.Callback
	rCall, wCall        js.Callback
	ctx, doc, canvasEl  js.Value
	opText              string
	highLightSource     bool
	pointStep           = 0.05
	order               drawOrderSlice
	debug               = false // If true, some debugging info is printed to the javascript console
)

func main() {
	// Initialise canvas
	doc = js.Global().Get("document")
	canvasEl = doc.Call("getElementById", "mycanvas")
	width = doc.Get("body").Get("clientWidth").Float()
	height = doc.Get("body").Get("clientHeight").Float()
	canvasEl.Call("setAttribute", "width", width)
	canvasEl.Call("setAttribute", "height", height)
	canvasEl.Set("tabIndex", 0) // Not sure if this is needed
	ctx = canvasEl.Call("getContext", "2d")

	// Set up the mouse click handler
	cCall = js.NewCallback(clickHandler)
	doc.Call("addEventListener", "mousedown", cCall)
	defer cCall.Release()

	// Set up the keypress handler
	renderActive = atomic.NewBool(false)
	kCall = js.NewCallback(keypressHandler)
	doc.Call("addEventListener", "keydown", kCall)
	defer kCall.Release()

	// Set up the mouse move handler
	mCall = js.NewCallback(moveHandler)
	doc.Call("addEventListener", "mousemove", mCall)
	defer mCall.Release()

	// Set the frame renderer going
	rCall = js.NewCallback(renderFrame)
	js.Global().Call("requestAnimationFrame", rCall)
	defer rCall.Release()

	// Set up the mouse wheel handler
	wCall = js.NewCallback(wheelHandler)
	doc.Call("addEventListener", "wheel", wCall)
	defer wCall.Release()

	// Set the operations processor going
	queue = make(chan Operation)
	go processOperations(queue)

	// Add the X/Y axes object to the world space
	worldSpace = append(worldSpace, importObject(axes, 0.0, 0.0, 0.0))

	// Create a graph object with the main data points on it
	// TODO: Allow user input of equation to graph?
	//       That will probably mean we need to pull in some general algebra system solver, to avoid having to write
	//       one just for this (!).  At a first glance, corywalker/expreduce seems like it might be a decent fit.
	var firstDeriv, graph Object
	var p Point
	graphLabeled := false
	for x := -2.1; x <= 2.2; x += 0.05 {
		p = Point{X: x, Y: x * x * x} // y = x^3
		if !graphLabeled {
			p.Label = " Equation: y = x³ "
			p.LabelAlign = "right"
			graphLabeled = true
		}
		graph.P = append(graph.P, p)
	}
	graph.C = "blue"
	graph.DrawOrder = 1
	graph.Name = "graph"
	worldSpace = append(worldSpace, importObject(graph, 0.0, 0.0, 0.0))

	// Create a graph object with the 1st order derivative points on it
	graphLabeled = false
	for x := -2.1; x <= 2.2; x += pointStep {
		p = Point{X: x, Y: 2 * (x * x)} // y = 2x^2
		if !graphLabeled {
			p.Label = " 1st order derivative: y = 2x² "
			p.LabelAlign = "right"
			graphLabeled = true
		}
		firstDeriv.P = append(firstDeriv.P, p)
	}
	firstDeriv.C = "green"
	firstDeriv.DrawOrder = 2
	firstDeriv.Name = "firstDeriv"
	worldSpace = append(worldSpace, importObject(firstDeriv, 0.0, 0.0, 0.0))

	// TODO: Generate points for the 2nd order derivative?

	// Sort the objects by draw order - this stops flickering of objects at same depth overwriting each other when drawn
	for i, j := range worldSpace {
		order = append(order, drawOrder{spaceNum: i, order: j.DrawOrder})
	}
	sort.Sort(drawOrderSlice(order))

	// Keep the application running
	done := make(chan struct{}, 0)
	<-done
}

// Simple mouse handler watching for people clicking on the source code link
func clickHandler(args []js.Value) {
	event := args[0]
	clientX := event.Get("clientX").Float()
	clientY := event.Get("clientY").Float()
	if debug {
		fmt.Printf("ClientX: %v  clientY: %v\n", clientX, clientY)
		if clientX > graphWidth && clientY > (height-40) {
			println("URL hit!")
		}
	}

	// If the user clicks the source code URL area, open the URL
	if clientX > graphWidth && clientY > (height-40) {
		w := js.Global().Call("open", sourceURL)
		if w == js.Null() {
			// Couldn't open a new window, so try loading directly in the existing one instead
			doc.Set("location", sourceURL)
		}
	}
}

// Returns an object whose points have been transformed into 3D world space XYZ co-ordinates.  Also assigns a number
// to each point
func importObject(ob Object, x float64, y float64, z float64) (translatedObject Object) {
	// X and Y translation matrix.  Translates the objects into the world space at the given X and Y co-ordinates
	translateMatrix := matrix{
		1, 0, 0, x,
		0, 1, 0, y,
		0, 0, 1, z,
		0, 0, 0, 1,
	}

	// Translate the points
	var pt Point
	for _, j := range ob.P {
		pt = Point{
			Label:      j.Label,
			LabelAlign: j.LabelAlign,
			X:          (translateMatrix[0] * j.X) + (translateMatrix[1] * j.Y) + (translateMatrix[2] * j.Z) + (translateMatrix[3] * 1),   // 1st col, top
			Y:          (translateMatrix[4] * j.X) + (translateMatrix[5] * j.Y) + (translateMatrix[6] * j.Z) + (translateMatrix[7] * 1),   // 1st col, upper middle
			Z:          (translateMatrix[8] * j.X) + (translateMatrix[9] * j.Y) + (translateMatrix[10] * j.Z) + (translateMatrix[11] * 1), // 1st col, lower middle
		}
		translatedObject.P = append(translatedObject.P, pt)
	}

	// Copy the remaining object info across
	translatedObject.C = ob.C
	translatedObject.Name = ob.Name
	translatedObject.DrawOrder = ob.DrawOrder
	for _, j := range ob.E {
		translatedObject.E = append(translatedObject.E, j)
	}
	for _, j := range ob.S {
		translatedObject.S = append(translatedObject.S, j)
	}

	return translatedObject
}

// Simple keyboard handler for catching the arrow, WASD, and numpad keys
// Key value info can be found here: https://developer.mozilla.org/en-US/docs/Web/API/KeyboardEvent/key/Key_Values
func keypressHandler(args []js.Value) {
	event := args[0]
	key := event.Get("key").String()
	if debug {
		fmt.Printf("Key is: %v\n", key)
	}

	// Don't add operations if one is already in progress
	stepSize := float64(25)
	if !renderActive.Load() {
		switch key {
		case "ArrowLeft", "a", "A", "4":
			queue <- Operation{op: ROTATE, t: 50, f: 12, X: 0, Y: -stepSize, Z: 0}
		case "ArrowRight", "d", "D", "6":
			queue <- Operation{op: ROTATE, t: 50, f: 12, X: 0, Y: stepSize, Z: 0}
		case "ArrowUp", "w", "W", "8":
			queue <- Operation{op: ROTATE, t: 50, f: 12, X: -stepSize, Y: 0, Z: 0}
		case "ArrowDown", "s", "S", "2":
			queue <- Operation{op: ROTATE, t: 50, f: 12, X: stepSize, Y: 0, Z: 0}
		case "7", "Home":
			queue <- Operation{op: ROTATE, t: 50, f: 12, X: -stepSize, Y: -stepSize, Z: 0}
		case "9", "PageUp":
			queue <- Operation{op: ROTATE, t: 50, f: 12, X: -stepSize, Y: stepSize, Z: 0}
		case "1", "End":
			queue <- Operation{op: ROTATE, t: 50, f: 12, X: stepSize, Y: -stepSize, Z: 0}
		case "3", "PageDown":
			queue <- Operation{op: ROTATE, t: 50, f: 12, X: stepSize, Y: stepSize, Z: 0}
		case "-":
			queue <- Operation{op: ROTATE, t: 50, f: 12, X: 0, Y: 0, Z: -stepSize}
		case "+":
			queue <- Operation{op: ROTATE, t: 50, f: 12, X: 0, Y: 0, Z: stepSize}
		}
	}
}

// Multiplies one matrix by another
func matrixMult(opMatrix matrix, m matrix) (resultMatrix matrix) {
	top0 := m[0]
	top1 := m[1]
	top2 := m[2]
	top3 := m[3]
	upperMid0 := m[4]
	upperMid1 := m[5]
	upperMid2 := m[6]
	upperMid3 := m[7]
	lowerMid0 := m[8]
	lowerMid1 := m[9]
	lowerMid2 := m[10]
	lowerMid3 := m[11]
	bot0 := m[12]
	bot1 := m[13]
	bot2 := m[14]
	bot3 := m[15]

	resultMatrix = matrix{
		(opMatrix[0] * top0) + (opMatrix[1] * upperMid0) + (opMatrix[2] * lowerMid0) + (opMatrix[3] * bot0), // 1st col, top
		(opMatrix[0] * top1) + (opMatrix[1] * upperMid1) + (opMatrix[2] * lowerMid1) + (opMatrix[3] * bot1), // 2nd col, top
		(opMatrix[0] * top2) + (opMatrix[1] * upperMid2) + (opMatrix[2] * lowerMid2) + (opMatrix[3] * bot2), // 3rd col, top
		(opMatrix[0] * top3) + (opMatrix[1] * upperMid3) + (opMatrix[2] * lowerMid3) + (opMatrix[3] * bot3), // 4th col, top

		(opMatrix[4] * top0) + (opMatrix[5] * upperMid0) + (opMatrix[6] * lowerMid0) + (opMatrix[7] * bot0), // 1st col, upper middle
		(opMatrix[4] * top1) + (opMatrix[5] * upperMid1) + (opMatrix[6] * lowerMid1) + (opMatrix[7] * bot1), // 2nd col, upper middle
		(opMatrix[4] * top2) + (opMatrix[5] * upperMid2) + (opMatrix[6] * lowerMid2) + (opMatrix[7] * bot2), // 3rd col, upper middle
		(opMatrix[4] * top3) + (opMatrix[5] * upperMid3) + (opMatrix[6] * lowerMid3) + (opMatrix[7] * bot3), // 4th col, upper middle

		(opMatrix[8] * top0) + (opMatrix[9] * upperMid0) + (opMatrix[10] * lowerMid0) + (opMatrix[11] * bot0), // 1st col, lower middle
		(opMatrix[8] * top1) + (opMatrix[9] * upperMid1) + (opMatrix[10] * lowerMid1) + (opMatrix[11] * bot1), // 2nd col, lower middle
		(opMatrix[8] * top2) + (opMatrix[9] * upperMid2) + (opMatrix[10] * lowerMid2) + (opMatrix[11] * bot2), // 3rd col, lower middle
		(opMatrix[8] * top3) + (opMatrix[9] * upperMid3) + (opMatrix[10] * lowerMid3) + (opMatrix[11] * bot3), // 4th col, lower middle

		(opMatrix[12] * top0) + (opMatrix[13] * upperMid0) + (opMatrix[14] * lowerMid0) + (opMatrix[15] * bot0), // 1st col, bottom
		(opMatrix[12] * top1) + (opMatrix[13] * upperMid1) + (opMatrix[14] * lowerMid1) + (opMatrix[15] * bot1), // 2nd col, bottom
		(opMatrix[12] * top2) + (opMatrix[13] * upperMid2) + (opMatrix[14] * lowerMid2) + (opMatrix[15] * bot2), // 3rd col, bottom
		(opMatrix[12] * top3) + (opMatrix[13] * upperMid3) + (opMatrix[14] * lowerMid3) + (opMatrix[15] * bot3), // 4th col, bottom
	}
	return resultMatrix
}

// Simple mouse handler watching for people moving the mouse over the source code link
func moveHandler(args []js.Value) {
	event := args[0]
	clientX := event.Get("clientX").Float()
	clientY := event.Get("clientY").Float()
	if debug {
		fmt.Printf("ClientX: %v  clientY: %v\n", clientX, clientY)
	}

	// If the mouse is over the source code link, let the frame renderer know to draw the url in bold
	if clientX > graphWidth && clientY > (height-40) {
		highLightSource = true
	} else {
		highLightSource = false
	}
}

// Animates the transformation operations
func processOperations(queue <-chan Operation) {
	for i := range queue {
		renderActive.Store(true)         // Mark rendering as now in progress
		parts := i.f                     // Number of parts to break each transformation into
		transformMatrix = identityMatrix // Reset the transform matrix
		switch i.op {
		case ROTATE: // Rotate the objects in world space
			// Divide the desired angle into a small number of parts
			if i.X != 0 {
				transformMatrix = rotateAroundX(transformMatrix, i.X/float64(parts))
			}
			if i.Y != 0 {
				transformMatrix = rotateAroundY(transformMatrix, i.Y/float64(parts))
			}
			if i.Z != 0 {
				transformMatrix = rotateAroundZ(transformMatrix, i.Z/float64(parts))
			}
			opText = fmt.Sprintf("Rotation. X: %0.2f Y: %0.2f Z: %0.2f", i.X, i.Y, i.Z)

		case SCALE:
			// Scale the objects in world space
			var xPart, yPart, zPart float64
			if i.X != 1 {
				xPart = ((i.X - 1) / float64(parts)) + 1
			}
			if i.Y != 1 {
				yPart = ((i.Y - 1) / float64(parts)) + 1
			}
			if i.Z != 1 {
				zPart = ((i.Z - 1) / float64(parts)) + 1
			}
			transformMatrix = scale(transformMatrix, xPart, yPart, zPart)
			opText = fmt.Sprintf("Scale. X: %0.2f Y: %0.2f Z: %0.2f", i.X, i.Y, i.Z)

		case TRANSLATE:
			// Translate (move) the objects in world space
			transformMatrix = translate(transformMatrix, i.X/float64(parts), i.Y/float64(parts), i.Z/float64(parts))
			opText = fmt.Sprintf("Translate (move). X: %0.2f Y: %0.2f Z: %0.2f", i.X, i.Y, i.Z)
		}

		// Apply each transformation, one small part at a time (this gives the animation effect)
		timeSlice := time.Millisecond * time.Duration(i.t/parts)
		for t := 0; t < int(parts); t++ {
			time.Sleep(timeSlice)
			for j, o := range worldSpace {
				var newPoints []Point

				// Transform each point of in the object
				for _, j := range o.P {
					newPoints = append(newPoints, transform(transformMatrix, j))
				}
				o.P = newPoints

				// Update the object in world space
				worldSpace[j] = o
			}
		}
		renderActive.Store(false)
		opText = "Complete."
	}
}

// Renders one frame of the animation
func renderFrame(args []js.Value) {
	// Handle window resizing
	curBodyW := doc.Get("body").Get("clientWidth").Float()
	curBodyH := doc.Get("body").Get("clientHeight").Float()
	if curBodyW != width || curBodyH != height {
		width, height = curBodyW, curBodyH
		canvasEl.Set("width", width)
		canvasEl.Set("height", height)
	}

	// Setup useful variables
	border := float64(2)
	gap := float64(3)
	left := border + gap
	top := border + gap
	graphWidth = width * 0.75
	graphHeight = height - 1
	centerX := graphWidth / 2
	centerY := graphHeight / 2

	// Clear the background
	ctx.Set("fillStyle", "white")
	ctx.Call("fillRect", 0, 0, width, height)

	// Draw grid lines
	step := math.Min(width, height) / 30
	ctx.Set("strokeStyle", "rgb(220, 220, 220)")
	ctx.Call("setLineDash", []interface{}{1, 3})
	for i := left; i < graphWidth-step; i += step {
		// Vertical dashed lines
		ctx.Call("beginPath")
		ctx.Call("moveTo", i+step, top)
		ctx.Call("lineTo", i+step, graphHeight)
		ctx.Call("stroke")
	}
	for i := top; i < graphHeight-step; i += step {
		// Horizontal dashed lines
		ctx.Call("beginPath")
		ctx.Call("moveTo", left, i+step)
		ctx.Call("lineTo", graphWidth-border, i+step)
		ctx.Call("stroke")
	}

	// Draw the axes
	var pointX, pointY float64
	ctx.Set("strokeStyle", "black")
	ctx.Set("lineWidth", "1")
	ctx.Call("setLineDash", []interface{}{})
	for _, o := range worldSpace {

		// Draw the surfaces
		ctx.Set("fillStyle", o.C)
		for _, l := range o.S {
			for m, n := range l {
				pointX = o.P[n].X
				pointY = o.P[n].Y
				if m == 0 {
					ctx.Call("beginPath")
					ctx.Call("moveTo", centerX+(pointX*step), centerY+((pointY*step)*-1))
				} else {
					ctx.Call("lineTo", centerX+(pointX*step), centerY+((pointY*step)*-1))
				}
			}
			ctx.Call("closePath")
			ctx.Call("fill")
		}

		// Draw the edges
		var point1X, point1Y, point2X, point2Y float64
		for _, l := range o.E {
			point1X = o.P[l[0]].X
			point1Y = o.P[l[0]].Y
			point2X = o.P[l[1]].X
			point2Y = o.P[l[1]].Y
			ctx.Call("beginPath")
			ctx.Call("moveTo", centerX+(point1X*step), centerY+((point1Y*step)*-1))
			ctx.Call("lineTo", centerX+(point2X*step), centerY+((point2Y*step)*-1))
			ctx.Call("stroke")
		}

		// Draw any point labels
		ctx.Set("fillStyle", "black")
		ctx.Set("font", "bold 14px serif")
		var px, py float64
		for _, l := range o.P {
			if l.Label != "" {
				ctx.Set("textAlign", l.LabelAlign)
				px = centerX + (l.X * step)
				py = centerY + ((l.Y * step) * -1)
				ctx.Call("fillText", l.Label, px, py)
			}
		}
	}

	// Draw the graph and derivatives
	ctx.Set("lineWidth", "2")
	ctx.Call("setLineDash", []interface{}{})
	var px, py float64
	numWld := len(worldSpace)
	for i := 0; i < numWld; i++ {
		o := worldSpace[order[i].spaceNum]
		if o.Name != "axes" {
			// Draw lines between the points
			ctx.Set("strokeStyle", o.C)
			ctx.Call("beginPath")
			for k, l := range o.P {
				px = centerX + (l.X * step)
				py = centerY + ((l.Y * step) * -1)
				if k == 0 {
					ctx.Call("moveTo", px, py)
				} else {
					ctx.Call("lineTo", px, py)
				}
			}
			ctx.Call("stroke")

			// Draw dots for the points
			ctx.Set("fillStyle", "black")
			for _, l := range o.P {
				px = centerX + (l.X * step)
				py = centerY + ((l.Y * step) * -1)
				ctx.Call("beginPath")
				ctx.Call("ellipse", px, py, 1, 1, 0, 0, 2*math.Pi)
				ctx.Call("fill")
				ctx.Call("stroke")
			}
		}
	}

	// Clear the information area (right side)
	ctx.Set("fillStyle", "white")
	ctx.Call("fillRect", graphWidth+1, 0, width, height)

	// Draw the text describing the current operation
	textY := top + 20
	ctx.Set("fillStyle", "black")
	ctx.Set("font", "bold 14px serif")
	ctx.Set("textAlign", "left")
	ctx.Call("fillText", "Operation:", graphWidth+20, textY)
	textY += 20
	ctx.Set("font", "14px sans-serif")
	ctx.Call("fillText", opText, graphWidth+20, textY)
	textY += 30

	// Add the help text about control keys and mouse zoom
	ctx.Set("fillStyle", "blue")
	ctx.Set("font", "14px sans-serif")
	ctx.Call("fillText", "Use wasd/numpad keys to rotate,", graphWidth+20, textY)
	textY += 20
	ctx.Call("fillText", "mouse wheel to zoom.", graphWidth+20, textY)
	textY += 30

	// Add the graph and derivatives information
	// TODO: Put the equation into a structure or string (TBD), and have everything automatically derived from that
	ctx.Set("fillStyle", "black")
	ctx.Set("font", "bold 14px serif")
	ctx.Call("fillText", "Equation", graphWidth+20, textY)
	textY += 20
	ctx.Set("font", "12px sans-serif")
	ctx.Call("fillText", "y = x³", graphWidth+40, textY)
	textY += 30

	// Add the derivatives information
	ctx.Set("font", "bold 14px serif")
	ctx.Call("fillText", "1st order derivative", graphWidth+20, textY)
	textY += 20
	ctx.Set("font", "12px sans-serif")
	ctx.Call("fillText", "y = 2x²", graphWidth+40, textY)

	// Clear the source code link area
	ctx.Set("fillStyle", "white")
	ctx.Call("fillRect", graphWidth+1, graphHeight-55, width, height)

	// Add the URL to the source code
	ctx.Set("fillStyle", "black")
	ctx.Set("font", "bold 14px serif")
	ctx.Call("fillText", "Source code:", graphWidth+20, graphHeight-35)
	ctx.Set("fillStyle", "blue")
	if highLightSource == true {
		ctx.Set("font", "bold 12px sans-serif")
	} else {
		ctx.Set("font", "12px sans-serif")
	}
	ctx.Call("fillText", sourceURL, graphWidth+20, graphHeight-15)

	// Draw a border around the graph area
	ctx.Call("setLineDash", []interface{}{})
	ctx.Set("lineWidth", "2")
	ctx.Set("strokeStyle", "white")
	ctx.Call("beginPath")
	ctx.Call("moveTo", 0, 0)
	ctx.Call("lineTo", width, 0)
	ctx.Call("lineTo", width, height)
	ctx.Call("lineTo", 0, height)
	ctx.Call("closePath")
	ctx.Call("stroke")
	ctx.Set("lineWidth", "2")
	ctx.Set("strokeStyle", "black")
	ctx.Call("beginPath")
	ctx.Call("moveTo", border, border)
	ctx.Call("lineTo", graphWidth, border)
	ctx.Call("lineTo", graphWidth, graphHeight)
	ctx.Call("lineTo", border, graphHeight)
	ctx.Call("closePath")
	ctx.Call("stroke")

	// Schedule the next frame render call
	js.Global().Call("requestAnimationFrame", rCall)
}

// Rotates a transformation matrix around the X axis by the given degrees
func rotateAroundX(m matrix, degrees float64) matrix {
	rad := (math.Pi / 180) * degrees // The Go math functions use radians, so we convert degrees to radians
	rotateXMatrix := matrix{
		1, 0, 0, 0,
		0, math.Cos(rad), -math.Sin(rad), 0,
		0, math.Sin(rad), math.Cos(rad), 0,
		0, 0, 0, 1,
	}
	return matrixMult(rotateXMatrix, m)
}

// Rotates a transformation matrix around the Y axis by the given degrees
func rotateAroundY(m matrix, degrees float64) matrix {
	rad := (math.Pi / 180) * degrees // The Go math functions use radians, so we convert degrees to radians
	rotateYMatrix := matrix{
		math.Cos(rad), 0, math.Sin(rad), 0,
		0, 1, 0, 0,
		-math.Sin(rad), 0, math.Cos(rad), 0,
		0, 0, 0, 1,
	}
	return matrixMult(rotateYMatrix, m)
}

// Rotates a transformation matrix around the Z axis by the given degrees
func rotateAroundZ(m matrix, degrees float64) matrix {
	rad := (math.Pi / 180) * degrees // The Go math functions use radians, so we convert degrees to radians
	rotateZMatrix := matrix{
		math.Cos(rad), -math.Sin(rad), 0, 0,
		math.Sin(rad), math.Cos(rad), 0, 0,
		0, 0, 1, 0,
		0, 0, 0, 1,
	}
	return matrixMult(rotateZMatrix, m)
}

// Scales a transformation matrix by the given X, Y, and Z values
func scale(m matrix, x float64, y float64, z float64) matrix {
	scaleMatrix := matrix{
		x, 0, 0, 0,
		0, y, 0, 0,
		0, 0, z, 0,
		0, 0, 0, 1,
	}
	return matrixMult(scaleMatrix, m)
}

// Transform the XYZ co-ordinates using the values from the transformation matrix
func transform(m matrix, p Point) (t Point) {
	top0 := m[0]
	top1 := m[1]
	top2 := m[2]
	top3 := m[3]
	upperMid0 := m[4]
	upperMid1 := m[5]
	upperMid2 := m[6]
	upperMid3 := m[7]
	lowerMid0 := m[8]
	lowerMid1 := m[9]
	lowerMid2 := m[10]
	lowerMid3 := m[11]
	//bot0 := m[12] // The fourth row values can be ignored for 3D matrices
	//bot1 := m[13]
	//bot2 := m[14]
	//bot3 := m[15]

	t.Label = p.Label
	t.LabelAlign = p.LabelAlign
	t.X = (top0 * p.X) + (top1 * p.Y) + (top2 * p.Z) + top3
	t.Y = (upperMid0 * p.X) + (upperMid1 * p.Y) + (upperMid2 * p.Z) + upperMid3
	t.Z = (lowerMid0 * p.X) + (lowerMid1 * p.Y) + (lowerMid2 * p.Z) + lowerMid3
	return
}

// Translates (moves) a transformation matrix by the given X, Y and Z values
func translate(m matrix, translateX float64, translateY float64, translateZ float64) matrix {
	translateMatrix := matrix{
		1, 0, 0, translateX,
		0, 1, 0, translateY,
		0, 0, 1, translateZ,
		0, 0, 0, 1,
	}
	return matrixMult(translateMatrix, m)
}

// Simple mouse handler watching for mouse wheel events
// Reference info can be found here: https://developer.mozilla.org/en-US/docs/Web/Events/wheel
func wheelHandler(args []js.Value) {
	event := args[0]
	wheelDelta := event.Get("deltaY").Float()
	scaleSize := 1 + (wheelDelta / 5)
	if debug {
		fmt.Printf("Wheel delta: %v, scaleSize: %v\n", wheelDelta, scaleSize)
	}

	// Don't add operations if one is already in progress
	if !renderActive.Load() {
		queue <- Operation{op: SCALE, t: 50, f: 12, X: scaleSize, Y: scaleSize, Z: scaleSize}
	}
}

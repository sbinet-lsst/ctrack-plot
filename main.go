// Copyright 2017 The sbinet-lsst Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// ctrack-plot is a program to plot the result of a C-Track run, taking
// a CSV file as input.
package main

import (
	"bytes"
	"encoding/csv"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"io"
	"log"
	"os"
	"runtime"
	"runtime/pprof"

	"golang.org/x/sync/errgroup"

	"github.com/gonum/plot/plotter"
	"github.com/gonum/plot/vg"
	"github.com/gonum/plot/vg/draw"
	"github.com/gonum/plot/vg/vgimg"

	"go-hep.org/x/hep/csvutil"
	"go-hep.org/x/hep/hbook"
	"go-hep.org/x/hep/hplot"
)

var (
	doGIF  = flag.Bool("gif", false, "create an animated GIF")
	doProf = flag.Bool("prof", false, "enable CPU profiling")
	beg    = flag.Int("beg", 450, "start")
	end    = flag.Int("end", 1600, "end")
	conc   = flag.Int("conc", 0, "number of CPU crunching goroutines")
	ofname = flag.String("o", "out.png", "output plot file")
)

func main() {
	log.SetPrefix("ctrack-plot ")
	log.SetFlags(0)

	flag.Parse()
	if *conc == 0 {
		*conc = 2 * runtime.NumCPU()
	}
	if *doProf {
		f, err := os.Create("cpu.prof")
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		err = pprof.StartCPUProfile(f)
		if err != nil {
			log.Fatal(err)
		}
		defer pprof.StopCPUProfile()
	}

	if flag.NArg() != 1 {
		log.Fatalf("need an input file")
	}

	f, err := os.Open(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	r := &fpReader{r: f}
	tbl := csvutil.Table{
		Reader: csv.NewReader(r),
	}
	defer tbl.Close()
	tbl.Reader.Comma = ';'
	tbl.Reader.Comment = '#'
	tbl.Reader.TrailingComma = true

	rows, err := tbl.ReadRows(0, -1)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	var tline Timeline
	irow := 0
	for rows.Next() {
		var tgt Targets
		err = rows.Scan(&tgt)
		if err != nil {
			log.Fatalf("error reading row %d: %v\n", irow, err)
		}
		if !pointOK(tgt) {
			log.Printf("error row %d:\n%#v", irow, tgt)
			continue
		}
		tline.T1 = append(tline.T1, Target{Time: tgt.Time, X: tgt.X1, Y: tgt.Y1, Z: tgt.Z1, Detected: tgt.D1})
		tline.T2 = append(tline.T2, Target{Time: tgt.Time, X: tgt.X2, Y: tgt.Y2, Z: tgt.Z2, Detected: tgt.D2})
		tline.T3 = append(tline.T3, Target{Time: tgt.Time, X: tgt.X3, Y: tgt.Y3, Z: tgt.Z3, Detected: tgt.D3})
		tline.T4 = append(tline.T4, Target{Time: tgt.Time, X: tgt.X4, Y: tgt.Y4, Z: tgt.Z4, Detected: tgt.D4})
		tline.T5 = append(tline.T5, Target{Time: tgt.Time, X: tgt.X5, Y: tgt.Y5, Z: tgt.Z5, Detected: tgt.D5})
		tline.T6 = append(tline.T6, Target{Time: tgt.Time, X: tgt.X6, Y: tgt.Y6, Z: tgt.Z6, Detected: tgt.D6})
		irow++
	}
	err = rows.Err()
	if err == io.EOF {
		err = nil
	}
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("read %d rows", irow)

	_, err = makePlot(tline, len(tline.T1), *ofname)
	if err != nil {
		log.Fatal(err)
	}

	if *doGIF {
		tline.Beg = *beg
		tline.End = *end
		err = makePlots(tline, "out.gif")
		if err != nil {
			log.Fatal(err)
		}
	}

	err = makeResPlot(tline.T1)
	if err != nil {
		log.Fatal(err)
	}
}

type Timeline struct {
	Beg, End int
	T1       []Target
	T2       []Target
	T3       []Target
	T4       []Target
	T5       []Target
	T6       []Target
}

type Target struct {
	Time     float64
	X        float64
	Y        float64
	Z        float64
	Detected bool
}

func pointOK(tgts Targets) bool {
	const (
		xmin = -800
		xmax = +800
		ymin = -800
		ymax = +800
		zmin = 2400
		zmax = 5000
	)
	for _, x := range []float64{tgts.X1, tgts.X2, tgts.X3, tgts.X4, tgts.X5, tgts.X6} {
		if xmax < x || x < xmin {
			return false
		}
	}
	for _, y := range []float64{tgts.Y1, tgts.Y2, tgts.Y3, tgts.Y4, tgts.Y5, tgts.Y6} {
		if ymax < y || y < ymin {
			return false
		}
	}
	for _, z := range []float64{tgts.Z1, tgts.Z2, tgts.Z3, tgts.Z4, tgts.Z5, tgts.Z6} {
		if zmax < z || z < zmin {
			return false
		}
	}
	return true
}

type Targets struct {
	Time       float64
	X1, Y1, Z1 float64
	D1         bool
	X2, Y2, Z2 float64
	D2         bool
	X3, Y3, Z3 float64
	D3         bool
	X4, Y4, Z4 float64
	D4         bool
	X5, Y5, Z5 float64
	D5         bool
	X6, Y6, Z6 float64
	D6         bool
}

func makePlots(tline Timeline, fname string) error {
	var g errgroup.Group
	n := tline.End - tline.Beg
	anim := gif.GIF{LoopCount: n}
	imgs := make([]*image.Paletted, n)
	throt := make(chan int, *conc)
	sum := make(chan int)
	go func() {
		v := 0
		for range sum {
			v++
			log.Printf("plots= %3d/%d", v, n)
		}
	}()
	for i := 0; i < n; i++ {
		j := i + 1
		g.Go(func() error {
			throt <- 1
			defer func() { <-throt }()
			src, err := makePlot(tline, j, "")
			if err != nil {
				return err
			}
			img, err := cnvToGIF(src)
			if err != nil {
				return err
			}
			imgs[j-1] = img
			sum <- 1
			return nil
		})
	}

	err := g.Wait()
	close(sum)
	if err != nil {
		return err
	}

	for _, img := range imgs {
		anim.Delay = append(anim.Delay, 0)
		anim.Image = append(anim.Image, img)
	}

	out, err := os.Create("out.gif")
	if err != nil {
		return err
	}
	defer out.Close()

	err = gif.EncodeAll(out, &anim)
	if err != nil {
		return err
	}
	return out.Close()
}

func makePlot(tline Timeline, size int, fname string) (image.Image, error) {
	tp, err := hplot.NewTiledPlot(draw.Tiles{Cols: 2, Rows: 2})
	if err != nil {
		return nil, err
	}

	const (
		xmin = -650
		xmax = +650
		ymin = -650
		ymax = +650
		zmin = 2510
		zmax = 2565
	)

	tp.Plots[3] = nil
	for i := range tp.Plots[:3] {
		p := tp.Plots[i]
		switch i {
		case 0:
			p.X.Label.Text = "X-pos"
			p.X.Min = xmin
			p.X.Max = xmax
			p.Y.Label.Text = "Y-pos"
			p.Y.Min = ymin
			p.Y.Max = ymax
		case 1:
			p.X.Label.Text = "Z-pos"
			p.X.Min = zmin
			p.X.Max = zmax
			p.Y.Label.Text = "Y-pos"
			p.Y.Min = ymin
			p.Y.Max = ymax
		case 2:
			p.X.Label.Text = "X-pos"
			p.X.Min = xmin
			p.X.Max = xmax
			p.Y.Label.Text = "Z-pos"
			p.Y.Min = zmin
			p.Y.Max = zmax
		}
	}
	for _, tgts := range [][]Target{
		tline.T1[tline.Beg : tline.Beg+size],
		tline.T2[tline.Beg : tline.Beg+size],
		tline.T3[tline.Beg : tline.Beg+size],
		tline.T4[tline.Beg : tline.Beg+size],
		tline.T5[tline.Beg : tline.Beg+size],
		tline.T6[tline.Beg : tline.Beg+size],
	} {
		err = fillTiles(tp, tgts)
		if err != nil {
			return nil, err
		}
	}

	c := vgimg.PngCanvas{Canvas: vgimg.New(20*vg.Centimeter, 20*vg.Centimeter)}
	tp.Draw(draw.New(c))
	if fname != "" {
		f, err := os.Create(fname)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		_, err = c.WriteTo(f)
		if err != nil {
			return nil, err
		}
	}

	return c.Image(), nil
}

func fillTiles(tp *hplot.TiledPlot, tgts []Target) error {
	title := fmt.Sprintf("Time = %8.4fs", tgts[len(tgts)-1].Time)
	for i := range tp.Plots[:3] {
		p := tp.Plots[i]
		p.Title.Text = title
		xys := make(plotter.XYs, len(tgts))
		last := make(plotter.XYs, 1)
		ii := len(tgts) - 1
		switch i {
		case 0:
			for i, tgt := range tgts {
				xys[i].X = tgt.X
				xys[i].Y = tgt.Y
			}
			last[0].X = tgts[ii].X
			last[0].Y = tgts[ii].Y
		case 1:
			for i, tgt := range tgts {
				xys[i].X = tgt.Z
				xys[i].Y = tgt.Y
			}
			last[0].X = tgts[ii].Z
			last[0].Y = tgts[ii].Y
		case 2:
			for i, tgt := range tgts {
				xys[i].X = tgt.X
				xys[i].Y = tgt.Z
			}
			last[0].X = tgts[ii].X
			last[0].Y = tgts[ii].Z
		}
		rpt, err := hplot.NewScatter(last)
		if err != nil {
			return err
		}
		rpt.Color = color.RGBA{255, 0, 0, 255}

		pts, err := hplot.NewScatter(xys[:ii])
		if err != nil {
			return err
		}
		pts.Color = color.Black
		pts.GlyphStyle.Shape = draw.CircleGlyph{}
		pts.GlyphStyle.Radius = 0.5

		p.Add(rpt)
		p.Add(pts)
		p.Add(hplot.NewGrid())
	}
	return nil
}

var gifOpts = gif.Options{NumColors: 256}

func cnvToGIF(src image.Image) (*image.Paletted, error) {
	var buf bytes.Buffer
	err := gif.Encode(&buf, src, &gifOpts)
	if err != nil {
		return nil, err
	}

	img, err := gif.Decode(&buf)
	if err != nil {
		return nil, err
	}
	return img.(*image.Paletted), nil
}

type fpReader struct {
	r io.Reader
}

func (r fpReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	for i := 0; i < n; i++ {
		v := p[i]
		if v == ',' {
			v = '.'
		}
		p[i] = v
	}
	return n, err
}

func makeResPlot(tgts []Target) error {
	var err error
	var (
		xmean = tgts[0].X
		ymean = tgts[0].Y
		zmean = tgts[0].Z
		delta = 2.5
	)
	h1x := hbook.NewH1D(100, xmean-delta, xmean+delta)
	h1y := hbook.NewH1D(100, ymean-delta, ymean+delta)
	h1z := hbook.NewH1D(100, zmean-delta, zmean+delta)
	for _, tgt := range tgts[:50] {
		h1x.Fill(tgt.X, 1)
		h1y.Fill(tgt.Y, 1)
		h1z.Fill(tgt.Z, 1)
	}

	tp, err := hplot.NewTiledPlot(draw.Tiles{
		Cols: 2,
		Rows: 2,
	})
	if err != nil {
		return err
	}

	for _, tbl := range []struct {
		name string
		pl   *hplot.Plot
		h1   *hbook.H1D
	}{
		{"X (cm)", tp.Plot(0, 0), h1x},
		{"Y (cm)", tp.Plot(0, 1), h1y},
		{"Z (cm)", tp.Plot(1, 0), h1z},
	} {
		hh, err := hplot.NewH1D(tbl.h1)
		if err != nil {
			return err
		}
		// hh.Infos.Style = hplot.HInfoSummary

		tbl.pl.X.Label.Text = tbl.name
		tbl.pl.Add(hh)
		tbl.pl.Add(hplot.NewGrid())
	}

	labeldata := []string{
		fmt.Sprintf(`entries  = %v
x-mean   = %v
x-stddev = %v
x-minmax = [%v; %v]

y-mean   = %v
y-stddev = %v
y-minmax = [%v; %v]

z-mean   = %v
z-stddev = %v
z-minmax = [%v; %v]
`,
			h1x.Entries(),
			h1x.XMean(), h1x.XStdDev(), h1x.XMin(), h1x.XMax(),
			h1y.XMean(), h1y.XStdDev(), h1y.XMin(), h1y.XMax(),
			h1z.XMean(), h1z.XStdDev(), h1z.XMin(), h1z.XMax(),
		),
	}
	xys := make(plotter.XYs, len(labeldata))
	for i := range xys {
		xys[i].X = 0
		xys[i].Y = float64(len(labeldata) - i)
	}
	labels, err := plotter.NewLabels(plotter.XYLabels{
		XYs:    xys,
		Labels: labeldata,
	})
	if err != nil {
		log.Fatal(err)
	}
	for i := range labels.TextStyle {
		sty := &labels.TextStyle[i]
		err := sty.Font.SetName("Courier")
		if err != nil {
			log.Fatal(err)
		}
	}
	tp.Plot(1, 1).HideAxes()
	tp.Plot(1, 1).X.Min = -0.5
	tp.Plot(1, 1).X.Max = 1
	tp.Plot(1, 1).Y.Min = -10
	tp.Plot(1, 1).Y.Max = +20
	tp.Plot(1, 1).Add(labels)

	err = tp.Save(20*vg.Centimeter, -1, "resolution.png")
	if err != nil {
		return err
	}

	return err
}

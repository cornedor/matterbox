package game

import (
	"image"
	"image/png"
	"os"
	"testing"
)

// TestDumpFrames is not an assertion, it is a window. Set GORILLA_DUMP to a
// directory and it writes the frames a game actually produces, which is the only
// way to review a renderer: the fidelity of a banana is not a thing a unit test
// can have an opinion about.
//
//	GORILLA_DUMP=/tmp/g go test ./internal/game -run TestDumpFrames
func TestDumpFrames(t *testing.T) {
	dir := os.Getenv("GORILLA_DUMP")
	if dir == "" {
		t.Skip("set GORILLA_DUMP=<dir> to write frames")
	}

	const pxW, pxH = 1280, 700 // 2× the field, so a 6×7 banana is legible on screen
	var r Renderer
	dump := func(name string, img *image.RGBA) {
		t.Helper()
		f, err := os.Create(dir + "/" + name + ".png")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
	}

	m := NewMatch(42)
	m.NextSeed = func() uint16 { return 42 }
	m.Join("joiner")

	dump("00-city", r.Render(m.World, nil, nil, nil, pxW, pxH))

	// A banana in flight, at four consecutive poses of its tumble.
	m.Launch(0, 45, 90)
	for i := range 8 {
		m.Step(gorillasTestDT)
		if i >= 4 {
			dump("01-flight-"+string(rune('a'+i-4)), r.Render(m.World, m.Shot, nil, nil, pxW, pxH))
		}
	}

	// A banana explosion, start to finish.
	mb := NewMatch(3)
	mb.Join("joiner")
	mb.Launch(0, 45, 70)
	if ev := resolve(mb); ev.Kind != EvBuilding {
		t.Fatalf("wanted a building hit to photograph, got %v", ev.Kind)
	}
	for i := 0; mb.Boom != nil; i++ {
		if i%4 == 0 {
			dump("02-boom-"+string(rune('a'+i/4)), r.Render(mb.World, nil, mb.Boom, nil, pxW, pxH))
		}
		mb.Step(gorillasTestDT)
	}
	dump("02-boom-z-after", r.Render(mb.World, nil, nil, nil, pxW, pxH))

	// And the gorilla blast, which is a different animal entirely.
	angle, power := aimAt(t, 42, 0, 1)
	mg := NewMatch(42)
	mg.NextSeed = func() uint16 { return 42 }
	mg.Join("joiner")
	mg.Launch(0, angle, power)
	if ev := resolve(mg); ev.Kind != EvRound {
		t.Fatalf("wanted a gorilla hit to photograph, got %v", ev.Kind)
	}
	for i := 0; mg.Boom != nil; i++ {
		if i%3 == 0 {
			dump("03-gorilla-"+string(rune('a'+i/3)), r.Render(mg.World, nil, mg.Boom, nil, pxW, pxH))
		}
		mg.Step(gorillasTestDT)
	}

	// The victory dance, on the cratered city, before the next one goes up.
	for i := 0; mg.Dance != nil; i++ {
		if i%3 == 0 && i/3 < 6 {
			dump("07-dance-"+string(rune('a'+i/3)), r.Render(mg.World, nil, nil, mg.Dance, pxW, pxH))
		}
		mg.Step(gorillasTestDT)
	}

	// The thrown pose: the arm is up as the banana leaves.
	mt := NewMatch(42)
	mt.Join("joiner")
	mt.Launch(0, 60, 80)
	mt.Step(gorillasTestDT)
	dump("04-throw", r.Render(mt.World, mt.Shot, nil, nil, pxW, pxH))

	// The sun, hit and appalled.
	ms := NewMatch(42)
	ms.Join("joiner")
	ms.World.SunHit = true
	dump("05-sun-shocked", r.Render(ms.World, nil, nil, nil, pxW, pxH))

	// The end of a match: the loser stays in the hole.
	mw := NewMatch(42)
	mw.NextSeed = func() uint16 { return 42 }
	mw.Join("joiner")
	for range WinScore {
		mw.State.Turn = 0
		mw.Launch(0, angle, power)
		resolve(mw)
		settle(mw)
	}
	if mw.State.Phase != PhaseOver {
		t.Fatalf("wanted a finished match to photograph, phase is %v", mw.State.Phase)
	}
	dump("06-match-over", r.Render(mw.World, nil, nil, nil, pxW, pxH))
}

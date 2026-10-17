package atlas

// StraightWalkableTiles exposes straightWalkable to the external tests.
func StraightWalkableTiles(m *Position, b Position) bool {
	return straightWalkable(m.Map, m.Map.Tile(m.X, m.Y), b.Map.Tile(b.X, b.Y), false)
}

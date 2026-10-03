package model

import "math"

// Sub returns b - a.
func (v Vec3) Sub(b Vec3) Vec3 {
	return Vec3{X: v.X - b.X, Y: v.Y - b.Y, Z: v.Z - b.Z}
}

// Length returns the vector magnitude.
func (v Vec3) Length() float64 {
	return math.Sqrt(v.X*v.X + v.Y*v.Y + v.Z*v.Z)
}

// Distance returns the distance between two positions.
func (v Vec3) Distance(b Vec3) float64 {
	return v.Sub(b).Length()
}

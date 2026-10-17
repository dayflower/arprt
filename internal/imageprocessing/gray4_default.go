//go:build !gray4_native4

package imageprocessing

// Gray4Native4 temporarily keeps native 4-bit output opt-in until corrected OG
// firmware passes physical acceptance. Remove this switch after that migration.
const Gray4Native4 = false

const EncoderVersion = "png-v3-gray4-2bit"

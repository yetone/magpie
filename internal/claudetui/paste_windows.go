package claudetui

// imagePadOS: a pseudo console may drop the bracketed-paste marks, so an
// image's path is sent as a read long enough to be a paste, over 800
// characters and under the 1024 bytes Windows 10's conhost can lose
// (imagePad).
const imagePadOS = 900

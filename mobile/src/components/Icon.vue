<template>
  <svg
    viewBox="0 0 24 24" :width="size" :height="size" fill="none" stroke="currentColor"
    :stroke-width="stroke" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"
  >
    <path v-for="(d, i) in PATHS[name] || []" :key="i" :d="d" />
    <circle v-for="(c, i) in CIRCLES[name] || []" :key="'c' + i" :cx="c[0]" :cy="c[1]" :r="c[2]" />
  </svg>
</template>

<script setup>
// 细线条图标，颜色跟随文字；手机端统一用这一套，避免混用 emoji/字符。
defineProps({
  name: { type: String, required: true },
  size: { type: [Number, String], default: 20 },
  stroke: { type: [Number, String], default: 1.8 },
})

const PATHS = {
  home: ['M3 10.5 12 3l9 7.5', 'M5 9v11h14V9'],
  chat: ['M4 5h16v11H8l-4 4z', 'M8 9h8', 'M8 12h5'],
  settings: ['M4 6h8', 'M16 6h4', 'M4 12h4', 'M12 12h8', 'M4 18h10', 'M18 18h2', 'M14 4v4', 'M10 10v4', 'M16 16v4'],
  refresh: ['M20 11a8 8 0 1 0-2.3 5.7', 'M20 4v7h-7'],
  back: ['M15 5l-7 7 7 7'],
  chevron: ['M9 6l6 6-6 6'],
  file: ['M6 3h8l4 4v14H6z', 'M14 3v5h5'],
  folder: ['M3 6h6l2 2h10v11H3z'],
  camera: ['M4 8h3l2-3h6l2 3h3v11H4z'],
  plus: ['M12 5v14', 'M5 12h14'],
  mic: ['M12 3a3 3 0 0 1 3 3v6a3 3 0 0 1-6 0V6a3 3 0 0 1 3-3z', 'M5 11a7 7 0 0 0 14 0', 'M12 18v3'],
  send: ['M12 19V5', 'M6 11l6-6 6 6'],
  stop: ['M8 8h8v8H8z'],
  x: ['M6 6l12 12', 'M18 6L6 18'],
  image: ['M4 5h16v14H4z', 'M4 16l5-5 4 4 3-3 4 4'],
  search: ['M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14z', 'M20 20l-4-4'],
  key: ['M14 10a4 4 0 1 0-4.2 4L4 20v-3h3v-3h2.5'],
  device: ['M6 3h12v18H6z', 'M10 18h4'],
  qr: ['M4 4h6v6H4z', 'M14 4h6v6h-6z', 'M4 14h6v6H4z', 'M14 14h3v3h-3z', 'M19 19h1v1h-1z', 'M14 20h1v1h-1z', 'M20 14h1v1h-1z'],
  copy: ['M8 8h11v11H8z', 'M5 16V5h11'],
  check: ['M5 12.5l4.5 4.5L19 7'],
}
const CIRCLES = {
  camera: [[12, 13, 3.2]],
  image: [[9, 9.5, 1.4]],
  search: [],
  key: [[15.5, 8.5, 0.4]],
}
</script>

import type { Theme } from 'vitepress'
import DefaultTheme from 'vitepress/theme-without-fonts'

import Layout from './Layout.vue'
import './custom.css'

export default {
  extends: DefaultTheme,
  Layout
} satisfies Theme

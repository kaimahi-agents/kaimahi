<script setup lang="ts">
import DefaultTheme from 'vitepress/theme-without-fonts'

import ketuMark from '../../../brand/ketu.svg'
import JourneyChooser from './JourneyChooser.vue'

const { Layout } = DefaultTheme
</script>

<template>
  <Layout>
    <template #nav-bar-title-before>
      <span class="kmx-nav-mark" aria-hidden="true"><span>K</span></span>
    </template>

    <template #home-hero-before>
      <aside class="docs-channel" aria-label="Documentation version">
        <span class="docs-channel__signal" aria-hidden="true"></span>
        <span><strong>Development docs</strong> from <code>main</code>; unreleased behavior may appear.</span>
        <a href="https://github.com/kaimahi-agents/kaimahi/tree/v0.8.1">Stable v0.8.1</a>
      </aside>
    </template>

    <template #doc-before>
      <aside class="docs-channel docs-channel--page" aria-label="Documentation version">
        <span class="docs-channel__signal" aria-hidden="true"></span>
        <span><strong>Development docs</strong> from <code>main</code>.</span>
        <a href="https://github.com/kaimahi-agents/kaimahi/tree/v0.8.1">Stable v0.8.1</a>
      </aside>
    </template>

    <template #home-hero-image>
      <div class="signal-map" aria-hidden="true">
        <span class="signal-map__orbit signal-map__orbit--outer"></span>
        <span class="signal-map__orbit signal-map__orbit--inner"></span>
        <span class="signal-map__route signal-map__route--one"></span>
        <span class="signal-map__route signal-map__route--two"></span>
        <span class="signal-map__node signal-map__node--one"></span>
        <span class="signal-map__node signal-map__node--two"></span>
        <span class="signal-map__node signal-map__node--three"></span>
        <span class="signal-map__core"><img :src="ketuMark" alt="" /></span>
      </div>
    </template>

    <template #home-features-before>
      <JourneyChooser />
    </template>
  </Layout>
</template>

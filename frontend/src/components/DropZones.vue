<script setup lang="ts">
// The three drop targets. Each zone's wrapper is the Wails drop target and
// carries the spacing, so the zones tile their container with no gaps: a drop
// outside every target is silently ignored by Wails.
defineProps<{
  // Pad the outer edges inside the zones, for when they fill the window.
  edgePadding?: boolean
}>()

const zones = [
  { id: 'regular', title: 'Upload Only', description: 'Upload files without adding to any album' },
  { id: 'album', title: 'Upload to Album', description: "Upload and add to a specific album (you'll enter the name)" },
  { id: 'auto-album', title: 'Auto Album', description: 'Upload and create one album per folder, named after the folder' },
]
</script>

<template>
  <div class="flex flex-col">
    <div
      v-for="(zone, index) in zones"
      :key="zone.id"
      data-file-drop-target
      :data-drop-zone="zone.id"
      :class="[
        'flex-1 min-h-0 flex py-1.5',
        edgePadding && 'px-4',
        edgePadding && index === 0 && 'pt-8',
        edgePadding && index === zones.length - 1 && 'pb-4',
      ]"
    >
      <div class="drop-zone flex-1 flex flex-col items-center justify-center border-2 border-dashed border-muted-foreground/50 rounded-xl transition-all duration-200">
        <h2 class="text-lg font-semibold select-none text-muted-foreground">
          {{ zone.title }}
        </h2>
        <p class="text-sm text-muted-foreground/70 mt-1 select-none text-center px-4">
          {{ zone.description }}
        </p>
      </div>
    </div>
  </div>
</template>

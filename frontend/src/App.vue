<script setup lang="ts">
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { TooltipProvider } from '@/components/ui/tooltip'
import {
  Sheet,
  SheetContent,
  SheetTrigger,
} from '@/components/ui/sheet'
import { useColorMode } from '@vueuse/core'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { UserPlus } from '@lucide/vue'
import { ConfigManager } from '../bindings/app/core'
import { Events } from '@wailsio/runtime'
import Button from "./components/ui/button/Button.vue"
import GoogleAccountSelect from './components/GoogleAccountSelect.vue'
import GoogleAuthSetup from "./components/GoogleAuthSetup.vue"
import './index.css'
import SettingsPanel from "./SettingsPanel.vue"
import Upload from './Upload.vue'
import { uploadManager } from './utils/UploadManager'
import Toaster from './components/ui/sonner/Sonner.vue'

import { toast } from "vue-sonner"

useColorMode().value = "dark"

const { state: uploadState } = uploadManager

// Drag state for dual drop zones
const isDraggingFiles = ref(false)

// Album upload flow state
const showAlbumInput = ref(false)
// The upload list stays up after a run until the user clears it.
const showQueue = computed(() => uploadState.isUploading || uploadState.items.length > 0)
const pendingFiles = ref<string[]>([])
const pendingFileCount = ref(0)

const selectedOption = ref('')
const options = ref<string[]>([])
const accountNeedsTokenBinding = ref<Record<string, boolean>>({})
const albumNameOrKey = ref('')
const tokenBindingEmail = ref('')
const isExtractingTokenBinding = ref(false)
const isAccountSetupOpen = ref(false)
const removingAccount = ref('')

watch(selectedOption, async (newValue) => {
  if (newValue) {
    try {
      await ConfigManager.SetSelected(newValue)
      await updateTokenBindingPrompt(newValue)
      console.log('Successfully updated selected value:', newValue)
    } catch (error) {
      console.error('Failed to update selected value:', error)
      toast.error('Failed to update selected account.')
    }
  } else {
    tokenBindingEmail.value = ''
  }
})

async function updateTokenBindingPrompt(email: string) {
  tokenBindingEmail.value = accountNeedsTokenBinding.value[email] ? email : ''
}

async function refreshCredentials() {
  try {
    const state = await ConfigManager.GetAccounts()
    const nextNeedsTokenBinding: Record<string, boolean> = {}
    const nextOptions = state.accounts.map(account => {
      nextNeedsTokenBinding[account.email] = account.needsTokenBinding
      return account.email
    })

    accountNeedsTokenBinding.value = nextNeedsTokenBinding
    options.value = nextOptions
    selectedOption.value = state.selected || ''
    if (state.selected) {
      await updateTokenBindingPrompt(state.selected)
    } else {
      tokenBindingEmail.value = ''
    }
  } catch (error) {
    console.error('Failed to refresh Google accounts:', error)
    toast.error('Could not refresh Google accounts', {
      description: error instanceof Error ? error.message : String(error),
    })
  }
}

async function addTokenBindingAliasFromADB() {
  if (!tokenBindingEmail.value) return

  isExtractingTokenBinding.value = true
  try {
    await ConfigManager.AddTokenBindingAliasFromADB(tokenBindingEmail.value)
    await refreshCredentials()
    tokenBindingEmail.value = ''
    toast.success('Token binding key added.')
  } catch (error) {
    console.error('Failed to add token binding key:', error)
    toast.error('Failed to add token binding key', {
      description: error instanceof Error ? error.message : String(error),
    })
  } finally {
    isExtractingTokenBinding.value = false
  }
}

async function removeCredentials(email: string) {
  removingAccount.value = email
  try {
    await ConfigManager.RemoveCredentials(email)

    const removedSelectedAccount = selectedOption.value === email
    await refreshCredentials()
    if (removedSelectedAccount && options.value.length > 0 && !selectedOption.value) {
      selectedOption.value = options.value[0]
    }
    toast.success('Credentials removed.')
    return true
  } catch (error) {
    console.error('Failed to remove credentials:', error)
    toast.error('Failed to remove credentials.')
    return false
  } finally {
    removingAccount.value = ''
  }
}

function openAccountSetup() {
  isAccountSetupOpen.value = true
}

onMounted(async () => {
  await refreshCredentials()

})



// Global drag event handlers to detect file dragging
let dragLeaveTimeout: ReturnType<typeof setTimeout> | null = null

const onDragEnter = (e: DragEvent) => {
  if (!e.dataTransfer?.types.includes('Files')) return
  
  // Clear any pending drag leave timeout
  if (dragLeaveTimeout) {
    clearTimeout(dragLeaveTimeout)
    dragLeaveTimeout = null
  }
  
  isDraggingFiles.value = true
}

const onDragOver = (e: DragEvent) => {
  if (!e.dataTransfer?.types.includes('Files')) return
  e.preventDefault()
  
  // Clear any pending drag leave timeout - we're still dragging
  if (dragLeaveTimeout) {
    clearTimeout(dragLeaveTimeout)
    dragLeaveTimeout = null
  }
}

const onDragLeave = (e: DragEvent) => {
  if (!e.dataTransfer?.types.includes('Files')) return
  
  // Use timeout to detect if we've truly left the window
  // dragover will cancel this if we're still in the window
  if (dragLeaveTimeout) {
    clearTimeout(dragLeaveTimeout)
  }
  dragLeaveTimeout = setTimeout(() => {
    isDraggingFiles.value = false
    dragLeaveTimeout = null
  }, 50)
}

const onDrop = () => {
  // Clear any pending timeout
  if (dragLeaveTimeout) {
    clearTimeout(dragLeaveTimeout)
    dragLeaveTimeout = null
  }
  
  // Delay resetting isDraggingFiles to allow Wails to process the drop target
  // before Vue re-renders and hides the drop zones
  setTimeout(() => {
    isDraggingFiles.value = false
  }, 100)
}

// Queue files; the album choice travels with this drop only.
const queueFiles = async (files: string[], albumName: string, albumAutoMode: boolean) => {
  try {
    await Events.Emit('startUpload', { files, albumName, albumAutoMode })
  } catch (error) {
    console.error('Failed to start upload:', error)
    toast.error('Failed to start upload', {
      description: error instanceof Error ? error.message : String(error),
    })
  }
}

// Album upload confirmation
const confirmAlbumUpload = async () => {
  if (!albumNameOrKey.value.trim()) return
  await queueFiles(pendingFiles.value, albumNameOrKey.value.trim(), false)
  showAlbumInput.value = false
  pendingFiles.value = []
  pendingFileCount.value = 0
  albumNameOrKey.value = '' // Reset for next upload
}

const cancelAlbumUpload = () => {
  showAlbumInput.value = false
  pendingFiles.value = []
  pendingFileCount.value = 0
  albumNameOrKey.value = '' // Reset on cancel too
}

// Handle album error event
const albumErrorHandler = (e: Event) => {
  const event = e as CustomEvent<{ AlbumName: string; Error: string }>
  const { AlbumName, Error } = event.detail
  // Check if it's a 404 error (album key not found)
  if (Error.includes('404')) {
    toast.error('Album not found', {
      description: `The album key "${AlbumName}" does not exist or is invalid.`,
    })
  } else {
    toast.error('Failed to create album', {
      description: `Album "${AlbumName}": ${Error}`,
    })
  }
}

// Show the first failure right away, then fold any that follow within a few
// seconds into one summary toast so a batch-wide failure cannot flood the screen.
const errorToastWindowMs = 4000
let suppressedErrors = 0
let errorWindowTimer: ReturnType<typeof setTimeout> | null = null

const uploadErrorHandler = (e: Event) => {
  const event = e as CustomEvent<{ FileName: string; Message: string }>
  const { FileName, Message } = event.detail
  if (errorWindowTimer) {
    suppressedErrors++
    return
  }
  toast.error(FileName ? `Upload failed: ${FileName}` : 'Upload failed', {
    description: Message.replace(/^Error:\s*/, ''),
    duration: 10000,
    important: true,
  })
  const flush = () => {
    if (suppressedErrors > 0) {
      toast.error(`${suppressedErrors} more upload(s) failed`, {
        description: 'See the Failed filter in the upload list.',
        duration: 10000,
      })
      suppressedErrors = 0
      errorWindowTimer = setTimeout(flush, errorToastWindowMs)
    } else {
      errorWindowTimer = null
    }
  }
  errorWindowTimer = setTimeout(flush, errorToastWindowMs)
}

onMounted(() => {
  document.addEventListener('dragenter', onDragEnter)
  document.addEventListener('dragleave', onDragLeave)
  document.addEventListener('dragover', onDragOver)
  document.addEventListener('drop', onDrop)
  window.addEventListener('albumError', albumErrorHandler)
  window.addEventListener('uploadError', uploadErrorHandler)

  // Listen for files-dropped event from backend
  Events.On('files-dropped', async (event: { data: { files: string[]; dropZone: string } }) => {
    const { files, dropZone } = event.data

    if (dropZone === 'album') {
      // Show album input screen
      pendingFiles.value = files
      pendingFileCount.value = files.length
      showAlbumInput.value = true
    } else {
      await queueFiles(files, '', dropZone === 'auto-album')
    }
  })
})

onUnmounted(() => {
  document.removeEventListener('dragenter', onDragEnter)
  document.removeEventListener('dragleave', onDragLeave)
  document.removeEventListener('dragover', onDragOver)
  document.removeEventListener('drop', onDrop)
  window.removeEventListener('albumError', albumErrorHandler)
  window.removeEventListener('uploadError', uploadErrorHandler)
  if (dragLeaveTimeout) {
    clearTimeout(dragLeaveTimeout)
  }
  if (errorWindowTimer) {
    clearTimeout(errorWindowTimer)
  }
})
</script>

<template>
  <main
    class="relative w-screen h-screen flex flex-col items-center"
    style="--wails-draggable: drag"
  >
    <!-- Drop zones, shown over any view while files are dragged in -->
    <div
      v-if="isDraggingFiles && options.length > 0"
      class="absolute inset-0 z-40 flex flex-col gap-3 p-6 bg-background/95"
      style="--wails-draggable: none"
    >
      <p
        v-if="showQueue"
        class="text-center text-xs text-muted-foreground select-none"
      >
        Files dropped now join the current queue
      </p>
      <div
        data-file-drop-target
        data-drop-zone="regular"
        class="flex-1 flex flex-col items-center justify-center border-2 border-dashed border-muted-foreground/50 rounded-xl transition-all duration-200 drop-zone"
      >
        <h2 class="text-xl font-semibold select-none text-muted-foreground">
          Upload Only
        </h2>
        <p class="text-sm text-muted-foreground/70 mt-2 select-none text-center px-4">
          Upload files without adding to any album
        </p>
      </div>
      <div
        data-file-drop-target
        data-drop-zone="album"
        class="flex-1 flex flex-col items-center justify-center border-2 border-dashed border-muted-foreground/50 rounded-xl transition-all duration-200 drop-zone"
      >
        <h2 class="text-xl font-semibold select-none text-muted-foreground">
          Upload to Album
        </h2>
        <p class="text-sm text-muted-foreground/70 mt-2 select-none text-center px-4">
          Upload and add to a specific album (you'll enter the name)
        </p>
      </div>
      <div
        data-file-drop-target
        data-drop-zone="auto-album"
        class="flex-1 flex flex-col items-center justify-center border-2 border-dashed border-muted-foreground/50 rounded-xl transition-all duration-200 drop-zone"
      >
        <h2 class="text-xl font-semibold select-none text-muted-foreground">
          Auto Album
        </h2>
        <p class="text-sm text-muted-foreground/70 mt-2 select-none text-center px-4">
          Upload and create albums automatically based on folder names
        </p>
      </div>
    </div>

    <!-- Album name prompt for files dropped on the album zone -->
    <div
      v-if="showAlbumInput"
      class="absolute inset-0 z-30 flex flex-col items-center justify-center gap-6 p-8 bg-background"
      style="--wails-draggable: none"
    >
      <h1 class="text-xl font-semibold select-none">
        Upload to Album
      </h1>
      <p class="text-muted-foreground select-none">
        {{ pendingFileCount }} file(s) ready to upload
      </p>

      <form
        class="flex flex-col gap-2 w-full max-w-xs"
        @submit.prevent="confirmAlbumUpload"
      >
        <Label
          for="album-input"
          class="text-muted-foreground text-sm"
        >Album name or key</Label>
        <Input
          id="album-input"
          v-model="albumNameOrKey"
          placeholder="Album name or AF1Qip... key"
          autofocus
        />
        <p class="text-[11px] text-muted-foreground">
          Using the same name again this session adds to the same album.
        </p>
        <div class="flex gap-4 justify-center mt-4">
          <Button
            type="button"
            variant="outline"
            class="cursor-pointer select-none"
            @click="cancelAlbumUpload"
          >
            Cancel
          </Button>
          <Button
            type="submit"
            class="cursor-pointer select-none"
            :disabled="!albumNameOrKey.trim()"
          >
            Upload
          </Button>
        </div>
      </form>
    </div>

    <!-- Upload queue -->
    <div
      v-if="showQueue"
      class="w-full h-full"
      data-file-drop-target
    >
      <Upload />
    </div>

    <!-- Home -->
    <div
      v-else
      class="w-screen h-screen flex flex-col items-center gap-4 max-w-md px-6 pt-30"
      data-file-drop-target
    >
      <template v-if="options.length === 0">
        <div class="flex max-w-xs flex-col items-center gap-4 text-center">
          <div class="flex size-11 items-center justify-center rounded-full bg-muted text-muted-foreground">
            <UserPlus class="size-5" />
          </div>
          <div class="flex flex-col gap-1">
            <h1 class="text-xl font-semibold select-none">
              Connect Google Photos
            </h1>
            <p class="text-sm text-muted-foreground select-none">
              Add an account before uploading photos and videos.
            </p>
          </div>
          <Button
            class="cursor-pointer select-none"
            @click="openAccountSetup"
          >
            Add Google account
          </Button>
        </div>
      </template>

      <template v-else>
        <h1 class="text-xl font-semibold select-none">
          Drop files to upload
        </h1>
        <GoogleAccountSelect
          v-model="selectedOption"
          :options="options"
          :removing-account="removingAccount"
          @item-removed="removeCredentials"
          @add="openAccountSetup"
        />
        <div
          v-if="tokenBindingEmail"
          class="w-full max-w-xs border rounded-lg p-3 flex flex-col gap-3"
          style="--wails-draggable: none"
        >
          <p class="text-sm text-muted-foreground">
            This credential needs a token binding key from the rooted Android device it was captured from.
          </p>
          <Button
            class="cursor-pointer select-none"
            :disabled="isExtractingTokenBinding"
            @click="addTokenBindingAliasFromADB"
          >
            {{ isExtractingTokenBinding ? 'Reading ADB...' : 'Read from ADB' }}
          </Button>
        </div>

        <Sheet>
          <SheetTrigger>
            <Button
              variant="outline"
              class="cursor-pointer select-none"
            >
              Settings
            </Button>
          </SheetTrigger>
          <SheetContent side="bottom">
            <TooltipProvider disable-hoverable-content>
              <SettingsPanel />
            </TooltipProvider>
          </SheetContent>
        </Sheet>
      </template>
    </div>
    <GoogleAuthSetup
      v-model:open="isAccountSetupOpen"
      @account-added="refreshCredentials"
    />
    <Toaster
      position="bottom-center"
      rich-colors
      expand
      :visible-toasts="4"
    />
  </main>
</template>

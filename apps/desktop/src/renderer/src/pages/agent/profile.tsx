import { useState, useEffect, useRef, useCallback } from 'react'
import { Button } from '@multica/ui/components/ui/button'
import { Input } from '@multica/ui/components/ui/input'
import { Textarea } from '@multica/ui/components/ui/textarea'
import {
  Combobox,
  ComboboxInput,
  ComboboxContent,
  ComboboxList,
  ComboboxItem,
  ComboboxEmpty,
} from '@multica/ui/components/ui/combobox'
import {
  Loader2,
  Check,
  AlertCircle,
  ChevronDown,
} from 'lucide-react'
import { ApiKeyDialog } from '../../components/api-key-dialog'
import { OAuthDialog } from '../../components/oauth-dialog'
import { useProviderStore } from '../../stores/provider'
import { toast } from '@multica/ui/components/ui/sonner'
import { cn } from '@multica/ui/lib/utils'

export default function ProfilePage() {
  const { providers, current, setProvider, refresh, loading: providerLoading } = useProviderStore()

  const [profileLoading, setProfileLoading] = useState(true)
  const [_saving, setSaving] = useState(false)
  const [name, setName] = useState('')
  const [userContent, setUserContent] = useState('')
  const [hasChanges, setHasChanges] = useState(false)
  const [originalName, setOriginalName] = useState('')
  const [originalUserContent, setOriginalUserContent] = useState('')

  const [providerDropdownOpen, setProviderDropdownOpen] = useState(false)
  const [switching, setSwitching] = useState(false)
  const [apiKeyDialogOpen, setApiKeyDialogOpen] = useState(false)
  const [oauthDialogOpen, setOauthDialogOpen] = useState(false)
  const [selectedProvider, setSelectedProvider] = useState<{
    id: string
    name: string
    authMethod: 'api-key' | 'oauth'
    loginCommand?: string
  } | null>(null)
  const dropdownRef = useRef<HTMLDivElement>(null)


  useEffect(() => {
    loadProfile()
  }, [])

  useEffect(() => {
    setHasChanges(name !== originalName || userContent !== originalUserContent)
  }, [name, userContent, originalName, originalUserContent])

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setProviderDropdownOpen(false)
      }
    }
    if (providerDropdownOpen) {
      document.addEventListener('mousedown', handleClickOutside)
    }
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [providerDropdownOpen])

  const loadProfile = async () => {
    setProfileLoading(true)
    try {
      const data = await window.electronAPI.profile.get()
      const loadedName = data.name ?? ''
      const loadedUserContent = data.userContent ?? ''
      setName(loadedName)
      setUserContent(loadedUserContent)
      setOriginalName(loadedName)
      setOriginalUserContent(loadedUserContent)
    } catch (err) {
      console.error('Failed to load profile:', err)
      toast.error('Failed to load profile')
    } finally {
      setProfileLoading(false)
    }
  }

  const handleSaveProfile = useCallback(async () => {
    setSaving(true)
    try {
      await window.electronAPI.profile.updateName(name)
      await window.electronAPI.profile.updateUser(userContent)
      setOriginalName(name)
      setOriginalUserContent(userContent)
      setHasChanges(false)
      toast.success('Profile saved')
    } catch (err) {
      console.error('Failed to save profile:', err)
      toast.error('Failed to save profile')
    } finally {
      setSaving(false)
    }
  }, [name, userContent])

  // Keep ref to latest save function
  const saveRef = useRef(handleSaveProfile)
  saveRef.current = handleSaveProfile

  // Show/hide persistent toast for unsaved changes
  useEffect(() => {
    const toastId = 'unsaved-changes'
    if (hasChanges) {
      toast('Unsaved changes', {
        id: toastId,
        duration: Infinity,
        action: {
          label: 'Save',
          onClick: () => saveRef.current()
        }
      })
    } else {
      toast.dismiss(toastId)
    }
  }, [hasChanges])

  const handleProviderClick = async (p: typeof providers[0]) => {
    if (!p.available) {
      setSelectedProvider({
        id: p.id,
        name: p.name,
        authMethod: p.authMethod,
        loginCommand: p.loginCommand,
      })
      setProviderDropdownOpen(false)
      if (p.authMethod === 'oauth') {
        setOauthDialogOpen(true)
      } else {
        setApiKeyDialogOpen(true)
      }
      return
    }
    setSwitching(true)
    setProviderDropdownOpen(false)
    const result = await setProvider(p.id)
    setSwitching(false)
    if (!result.ok) {
      toast.error('Failed to switch provider')
    }
  }

  const handleModelSelect = async (model: string) => {
    if (!model || model === current?.model || !current?.provider) return
    setSwitching(true)
    const result = await setProvider(current.provider, model)
    setSwitching(false)
    if (!result.ok) {
      toast.error('Failed to switch model')
    }
  }

  if (profileLoading || providerLoading) {
    return (
      <div className="h-full flex items-center justify-center">
        <Loader2 className="size-5 animate-spin text-muted-foreground" />
      </div>
    )
  }

  const currentProvider = providers.find(p => p.id === current?.provider)

  return (
    <div className="h-full overflow-auto">
      <div className="container p-6">
        {/* Page Header */}
        <div className="mb-8">
          <h1 className="text-lg font-medium">Profile</h1>
          <p className="text-sm text-muted-foreground">
            Configure your agent's identity and the model that powers it.
          </p>
        </div>

        {/* Model Section */}
        <section className="mb-8">
          <div className="mb-3">
            <h2 className="text-sm font-medium">Model</h2>
            <p className="text-xs text-muted-foreground">AI model for your agent</p>
          </div>

          <div className="rounded-lg border bg-card">
            {/* Provider Row */}
            <div className="flex items-center justify-between px-4 py-3 border-b" ref={dropdownRef}>
              <div>
                <div className="text-sm font-medium">Provider</div>
                <div className="text-xs text-muted-foreground">LLM API connection</div>
              </div>
              <div className="relative">
                <Button
                  variant="outline"
                  size="sm"
                  className="h-8 gap-1.5"
                  onClick={() => setProviderDropdownOpen(!providerDropdownOpen)}
                  disabled={switching}
                >
                  {current?.available ? (
                    <Check className="size-3 text-green-500" />
                  ) : (
                    <AlertCircle className="size-3 text-yellow-500" />
                  )}
                  <span>{current?.providerName ?? 'Select'}</span>
                  <ChevronDown className={cn(
                    'size-3.5 text-muted-foreground transition-transform',
                    providerDropdownOpen && 'rotate-180'
                  )} />
                </Button>

                {providerDropdownOpen && (
                  <div className="absolute right-0 top-full mt-1 z-10 bg-popover border border-border rounded-lg shadow-lg p-1.5 min-w-[200px]">
                    {providers.map((p) => (
                      <button
                        key={p.id}
                        className={cn(
                          'w-full flex items-center gap-2 px-3 py-2 rounded text-left text-sm transition-colors',
                          p.id === current?.provider
                            ? 'bg-accent text-accent-foreground'
                            : 'hover:bg-accent/50',
                          !p.available && 'opacity-50'
                        )}
                        onClick={() => handleProviderClick(p)}
                        disabled={switching}
                      >
                        <span className={cn(
                          'size-2 rounded-full flex-shrink-0',
                          p.available ? 'bg-green-500' : 'bg-muted-foreground/40'
                        )} />
                        <span>{p.name}</span>
                      </button>
                    ))}
                  </div>
                )}
              </div>
            </div>

            {/* Model Row - with Combobox */}
            {currentProvider && (
              <div className="flex items-center justify-between px-4 py-3">
                <div>
                  <div className="text-sm font-medium">Model</div>
                  <div className="text-xs text-muted-foreground">Select or enter model ID</div>
                </div>
                <Combobox
                  items={currentProvider?.models ?? []}
                  value={current?.model ?? ''}
                  onValueChange={(value) => {
                    if (value) handleModelSelect(value)
                  }}
                  disabled={switching}
                >
                  <ComboboxInput
                    placeholder="Select model"
                    className="w-48 h-8 text-sm font-mono"
                  />
                  <ComboboxContent>
                    <ComboboxEmpty>No models found</ComboboxEmpty>
                    <ComboboxList>
                      {(model) => (
                        <ComboboxItem key={model} value={model}>
                          {model}
                        </ComboboxItem>
                      )}
                    </ComboboxList>
                  </ComboboxContent>
                </Combobox>
              </div>
            )}
          </div>

          {!current?.available && (
            <p className="text-xs text-yellow-600 dark:text-yellow-400 mt-2">
              Select a provider and add your API key to enable your agent.
            </p>
          )}
        </section>

        {/* Identity Section */}
        <section className="mb-8">
          <div className="mb-3">
            <h2 className="text-sm font-medium">Identity</h2>
            <p className="text-xs text-muted-foreground">How your agent presents itself</p>
          </div>

          <div className="rounded-lg border bg-card">
            <div className="flex items-center justify-between px-4 py-3">
              <div className="flex-1 mr-4">
                <div className="text-sm font-medium mb-1">Agent Name</div>
                <div className="text-xs text-muted-foreground">Personalize interactions</div>
              </div>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="My Assistant"
                className="h-8 w-48 text-sm"
              />
            </div>
          </div>
        </section>

        {/* Personalization Section */}
        <section className="mb-8">
          <div className="mb-3">
            <h2 className="text-sm font-medium">Personalization</h2>
            <p className="text-xs text-muted-foreground">Help the agent understand you better</p>
          </div>

          <Textarea
            value={userContent}
            onChange={(e) => setUserContent(e.target.value)}
            placeholder="- I'm a frontend developer&#10;- I prefer TypeScript&#10;- Please respond in Chinese"
            className="min-h-[120px] font-mono text-sm"
          />
        </section>

      </div>

      {/* Dialogs */}
      {selectedProvider && selectedProvider.authMethod === 'api-key' && (
        <ApiKeyDialog
          open={apiKeyDialogOpen}
          onOpenChange={setApiKeyDialogOpen}
          providerId={selectedProvider.id}
          providerName={selectedProvider.name}
          onSuccess={async () => {
            await refresh()
            const result = await setProvider(selectedProvider.id)
            if (!result.ok) {
              console.error('Failed to switch provider:', result.error)
            }
          }}
        />
      )}

      {selectedProvider && selectedProvider.authMethod === 'oauth' && (
        <OAuthDialog
          open={oauthDialogOpen}
          onOpenChange={setOauthDialogOpen}
          providerId={selectedProvider.id}
          providerName={selectedProvider.name}
          loginCommand={selectedProvider.loginCommand}
          onSuccess={async () => {
            await refresh()
            const result = await setProvider(selectedProvider.id)
            if (!result.ok) {
              console.error('Failed to switch provider:', result.error)
            }
          }}
        />
      )}
    </div>
  )
}

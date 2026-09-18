param(
    # Leave empty to resolve the container from Compose's own service label, which
    # works whatever the project is called. Set this (or RANSOMWARE_BOT_CONTAINER_NAME)
    # only to pin an explicit container name, e.g. when container_name is set by hand.
    [string]$ContainerName = $env:RANSOMWARE_BOT_CONTAINER_NAME,
    [string]$ComposeService = $(if ($env:RANSOMWARE_BOT_COMPOSE_SERVICE) { $env:RANSOMWARE_BOT_COMPOSE_SERVICE } else { "ransomware-news-bot" }),
    [string]$ComposeProject = $env:RANSOMWARE_BOT_COMPOSE_PROJECT,
    [string]$WebhookUrl = $env:HEALTH_ALERT_WEBHOOK_URL,
    [ValidateSet("slack", "discord", "generic")]
    [string]$WebhookType = $(if ($env:HEALTH_ALERT_WEBHOOK_TYPE) { $env:HEALTH_ALERT_WEBHOOK_TYPE } else { "generic" }),
    [string]$ExpectedStatus = "healthy",
    [string]$StatusOverride = "",
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

# Filled in by Get-ContainerHealthStatus once the container is resolved; until then the
# alert text falls back to naming what we looked for rather than printing an empty string.
$script:ResolvedContainerName = ""
function Get-ContainerDisplayName {
    if ($script:ResolvedContainerName) { return $script:ResolvedContainerName }
    if ($ContainerName) { return $ContainerName }
    return "compose service $ComposeService"
}

function Resolve-ContainerName {
    # Compose v2 names a container <project>-<service>-1, and <project> defaults to the
    # directory the repository was cloned into -- so a fixed name like "ransomware-bot"
    # never matches a default deployment. The service label is stable regardless: Compose
    # stamps com.docker.compose.service on every container it manages, and the service name
    # comes from docker-compose.yml, not from the directory.
    if ($ContainerName) {
        return $ContainerName
    }

    $filters = @("--filter", "label=com.docker.compose.service=$ComposeService")
    if ($ComposeProject) {
        $filters += @("--filter", "label=com.docker.compose.project=$ComposeProject")
    }

    $found = & docker ps --all $filters --format "{{.Names}}" 2>&1
    if ($LASTEXITCODE -ne 0) {
        return $null
    }

    $names = @($found | Where-Object { $_ -and ($_ -as [string]).Trim() } | ForEach-Object { ($_ -as [string]).Trim() })
    if ($names.Count -eq 0) {
        return $null
    }
    if ($names.Count -gt 1) {
        # Two projects running this compose file on one host. Guessing would alert on the
        # wrong container, so name them and let the caller disambiguate.
        throw ("Multiple containers match Compose service '$ComposeService': " + ($names -join ", ") +
               ". Pass -ComposeProject (or RANSOMWARE_BOT_COMPOSE_PROJECT) to pick one, " +
               "or -ContainerName to pin an exact container.")
    }
    return $names[0]
}

function Get-ContainerHealthStatus {
    if ($StatusOverride) {
        return @{
            Status = $StatusOverride
            Detail = "status override"
        }
    }

    $resolved = Resolve-ContainerName
    if (-not $resolved) {
        return @{
            Status = "missing"
            Detail = "no container matches Compose service '$ComposeService' (and no explicit -ContainerName was given)"
        }
    }
    $script:ResolvedContainerName = $resolved

    $format = "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}"
    $output = & docker inspect --format $format $resolved 2>&1
    if ($LASTEXITCODE -ne 0) {
        return @{
            Status = "missing"
            Detail = ($output -join "`n")
        }
    }

    return @{
        Status = (($output | Select-Object -First 1) -as [string]).Trim()
        Detail = ""
    }
}

function New-HealthAlertPayload {
    param(
        [string]$Status,
        [string]$Detail
    )

    $timestamp = (Get-Date).ToUniversalTime().ToString("o")
    $displayName = Get-ContainerDisplayName
    $message = "Ransomware News Bot container '$displayName' health is '$Status' (expected '$ExpectedStatus')."
    if ($Detail) {
        $message = "$message Detail: $Detail"
    }

    switch ($WebhookType.ToLowerInvariant()) {
        "slack" {
            return @{
                text = $message
                blocks = @(
                    @{
                        type = "section"
                        text = @{
                            type = "mrkdwn"
                            text = "*Ransomware News Bot health alert*`n$message"
                        }
                    },
                    @{
                        type = "context"
                        elements = @(
                            @{
                                type = "mrkdwn"
                                text = "Container: ``$displayName`` | Status: ``$Status`` | $timestamp"
                            }
                        )
                    }
                )
            } | ConvertTo-Json -Depth 6 -Compress
        }
        "discord" {
            return @{
                content = $message
                embeds = @(
                    @{
                        title = "Ransomware News Bot health alert"
                        description = $message
                        color = 16753920
                        timestamp = $timestamp
                    }
                )
            } | ConvertTo-Json -Depth 6 -Compress
        }
        default {
            return @{
                service = "ransomware-news-bot"
                container = $displayName
                status = $Status
                expected_status = $ExpectedStatus
                detail = $Detail
                timestamp = $timestamp
                text = $message
            } | ConvertTo-Json -Depth 6 -Compress
        }
    }
}

$health = Get-ContainerHealthStatus
if ($health.Status -eq $ExpectedStatus) {
    Write-Host "Container '$(Get-ContainerDisplayName)' health is '$($health.Status)'."
    exit 0
}

$payload = New-HealthAlertPayload -Status $health.Status -Detail $health.Detail
if ($DryRun) {
    Write-Output $payload
    exit 2
}

if (-not $WebhookUrl) {
    Write-Error "Container '$(Get-ContainerDisplayName)' health is '$($health.Status)', but HEALTH_ALERT_WEBHOOK_URL is not set."
    exit 2
}

Invoke-RestMethod -Method Post -Uri $WebhookUrl -ContentType "application/json" -Body $payload | Out-Null
Write-Host "Sent health alert for container '$(Get-ContainerDisplayName)' status '$($health.Status)'."
exit 2
